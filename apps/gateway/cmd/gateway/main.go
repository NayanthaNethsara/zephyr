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
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/outbox"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store/sqlite"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/telemetry"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/transport/webhook"
)

const (
	shutdownTimeout = 30 * time.Second
	janitorInterval = time.Minute
	pollInterval    = 500 * time.Millisecond
	batchSize       = 50
	publishTimeout  = 10 * time.Second
	readTimeout     = 30 * time.Second
	writeTimeout    = 30 * time.Second
	idleTimeout     = 2 * time.Minute
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

	publisher := webhook.New(cfg.WebhookURL, cfg.WebhookSigningSecret)
	defer func() { _ = publisher.Close() }()

	metrics := telemetry.New()
	metrics.WatchOutbox(db)

	service := core.NewService(db, cfg.TransportTopicPrefix, cfg.DedupTTL)

	worker := outbox.NewWorker(db, publisher, outbox.Settings{
		MaxAttempts:    cfg.RetryMaxAttempts,
		BaseDelay:      cfg.RetryBaseDelay,
		PollInterval:   pollInterval,
		BatchSize:      batchSize,
		PublishTimeout: publishTimeout,
	}, metrics, logger)
	sweeper := janitor.New(db, janitorInterval, cfg.FailedRetention, logger)

	var background sync.WaitGroup
	for _, job := range []func(context.Context){worker.Run, sweeper.Run} {
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
		logger.Info("zephyr listening", "addr", server.Addr, "transport", publisher.Name())
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
