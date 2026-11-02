package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/api"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/config"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/janitor"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/meta"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/outbox"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store/sqlite"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/telemetry"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/transport"
	natstransport "github.com/NayanthaNethsara/zephyr/apps/gateway/internal/transport/nats"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/transport/webhook"
)

const (
	shutdownTimeout  = 30 * time.Second
	janitorInterval  = time.Minute
	pollInterval     = 500 * time.Millisecond
	batchSize        = 50
	publishTimeout   = 10 * time.Second
	sessionRetention = 7 * 24 * time.Hour
	graphBaseURL     = "https://graph.facebook.com"
	readTimeout      = 30 * time.Second
	writeTimeout     = 30 * time.Second
	idleTimeout      = 2 * time.Minute
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "zephyr:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := sqlite.Open(ctx, cfg.SQLitePath())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	publisher, subscriber, err := newTransport(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = publisher.Close() }()

	metrics := telemetry.New()
	metrics.WatchOutbox(db)

	service := core.NewService(db, cfg.TransportTopicPrefix, cfg.DedupTTL, cfg.PhoneHashKey)

	worker := outbox.NewWorker(db, publisher, outbox.Settings{
		MaxAttempts:    cfg.RetryMaxAttempts,
		BaseDelay:      cfg.RetryBaseDelay,
		PollInterval:   pollInterval,
		BatchSize:      batchSize,
		PublishTimeout: publishTimeout,
	}, metrics, logger)
	sweeper := janitor.New(cleanupTasks(db, cfg), janitorInterval, logger)

	var failedEvents api.FailedEventProvider
	if len(cfg.APIKeyList()) > 0 {
		failedEvents = core.NewFailedEventService(db)
	}
	sending := newSending(cfg, db, metrics, logger)

	if subscriber != nil && sending.queue != nil {
		consumer := api.NewOutboundConsumer(sending.queue, metrics, logger)
		if err := subscriber.Subscribe(ctx, cfg.TransportTopicPrefix+".outbound", consumer.Handle); err != nil {
			return err
		}
	}

	jobs := []func(context.Context){worker.Run, sweeper.Run}
	if sending.worker != nil {
		jobs = append(jobs, sending.worker.Run)
	}
	var background sync.WaitGroup
	for _, job := range jobs {
		background.Add(1)
		go func() {
			defer background.Done()
			job(ctx)
		}()
	}
	defer background.Wait()

	server := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Port),
		Handler: api.NewRouter(api.Deps{
			Ingestor:        service,
			Recorder:        metrics,
			MetricsHandler:  metrics.Handler(),
			Sender:          sending.sender,
			Media:           sending.media,
			Events:          failedEvents,
			Sessions:        sending.sessions,
			Async:           sending.async,
			APIKeys:         cfg.APIKeyList(),
			MetaAppSecret:   cfg.MetaAppSecret,
			MetaVerifyToken: cfg.MetaVerifyToken,
			Logger:          logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("zephyr listening", "addr", server.Addr, "transport", publisher.Name(), "sending", cfg.SendingEnabled())
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		stop()
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func newTransport(ctx context.Context, cfg config.Config) (transport.Transport, transport.Subscriber, error) {
	if cfg.Transport == "nats" {
		broker, err := natstransport.New(ctx, cfg.NATSURL, cfg.TransportTopicPrefix)
		if err != nil {
			return nil, nil, err
		}
		return broker, broker, nil
	}
	return webhook.New(cfg.WebhookURL, cfg.WebhookSigningSecret), nil, nil
}

type sending struct {
	sender   api.MessageSender
	media    api.MediaProvider
	sessions api.SessionProvider
	async    api.AsyncSender
	queue    api.OutboundQueue
	worker   *outbox.SendWorker
}

func newSending(cfg config.Config, db *sqlite.Store, metrics *telemetry.Metrics, logger *slog.Logger) sending {
	if !cfg.SendingEnabled() {
		return sending{}
	}

	graphClient := meta.NewClient(graphBaseURL, cfg.MetaAPIVersion, cfg.WAAccessToken)
	sessions := core.NewSessionService(db, cfg.PhoneHashKey, cfg.WAPhoneNumberID)
	sender := core.NewSender(core.SenderDeps{
		Messenger:   graphClient,
		Idempotency: db,
		Sessions:    sessions,
		Limiter:     core.NewBucketLimiter(cfg.RateLimitMPS),
	}, cfg.WAPhoneNumberID, cfg.IdempotencyTTL)
	async := core.NewAsyncSender(sender, db, core.AsyncSettings{
		PhoneNumberID: cfg.WAPhoneNumberID,
		TopicPrefix:   cfg.TransportTopicPrefix,
		MaxAttempts:   cfg.RetryMaxAttempts,
		RetryDelay:    func(attempts int) time.Duration { return outbox.Backoff(attempts, cfg.RetryBaseDelay) },
	}, metrics, logger)

	return sending{
		sender:   sender,
		media:    core.NewMediaService(graphClient, graphClient, graphClient, cfg.WAPhoneNumberID),
		sessions: sessions,
		async:    async,
		queue:    async,
		worker:   outbox.NewSendWorker(db, async, pollInterval, batchSize, logger),
	}
}

func cleanupTasks(db *sqlite.Store, cfg config.Config) []janitor.Task {
	return []janitor.Task{
		{Name: "expired rows", Run: func(ctx context.Context, now time.Time) error {
			return db.DeleteExpired(ctx, now, now.Add(-cfg.FailedRetention))
		}},
		{Name: "expired sessions", Run: func(ctx context.Context, now time.Time) error {
			return db.DeleteExpiredSessions(ctx, now.Add(-sessionRetention))
		}},
		{Name: "expired send jobs", Run: func(ctx context.Context, now time.Time) error {
			return db.DeleteExpiredSends(ctx, now.Add(-cfg.IdempotencyTTL))
		}},
	}
}
