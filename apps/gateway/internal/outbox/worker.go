package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/transport"
)

type Settings struct {
	MaxAttempts    int
	BaseDelay      time.Duration
	PollInterval   time.Duration
	BatchSize      int
	PublishTimeout time.Duration
}

type Recorder interface {
	DeliveryAttempt(topic, result string, took time.Duration)
	EventDelivered(topic string, lag time.Duration)
}

const (
	resultSuccess = "success"
	resultRetry   = "retry"
	resultFailed  = "failed"
)

type Worker struct {
	store     store.Store
	transport transport.Transport
	settings  Settings
	recorder  Recorder
	logger    *slog.Logger
	now       func() time.Time
}

func NewWorker(s store.Store, t transport.Transport, settings Settings, recorder Recorder, logger *slog.Logger) *Worker {
	return &Worker{
		store:     s,
		transport: t,
		settings:  settings,
		recorder:  recorder,
		logger:    logger,
		now:       time.Now,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.settings.PollInterval)
	defer ticker.Stop()

	for {
		w.drain(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		entries, err := w.store.ListDue(ctx, w.now(), w.settings.BatchSize)
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Error("list due outbox entries", "error", err)
			}
			return
		}

		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}
			w.deliver(ctx, entry)
		}

		if len(entries) < w.settings.BatchSize {
			return
		}
	}
}

func (w *Worker) deliver(ctx context.Context, entry store.OutboxEntry) {
	publishCtx, cancel := context.WithTimeout(ctx, w.settings.PublishTimeout)
	started := w.now()
	err := w.transport.Publish(publishCtx, entry.Topic, entry.Key, entry.Payload)
	cancel()
	took := w.now().Sub(started)

	if err == nil {
		w.recordSuccess(ctx, entry, took)
		return
	}
	if ctx.Err() != nil {
		return
	}

	w.recordFailure(ctx, entry, took, err)
}

func (w *Worker) recordSuccess(ctx context.Context, entry store.OutboxEntry, took time.Duration) {
	w.recorder.DeliveryAttempt(entry.Topic, resultSuccess, took)

	if err := w.store.Delete(ctx, entry.ID); err != nil {
		w.logger.Error("delete delivered entry", "event_id", entry.ID, "error", err)
		return
	}

	lag := w.now().Sub(entry.CreatedAt)
	w.recorder.EventDelivered(entry.Topic, lag)
	w.logger.Info("event delivered",
		"event_id", entry.ID,
		"topic", entry.Topic,
		"attempts", entry.Attempts+1,
		"duration_ms", took.Milliseconds(),
		"lag_ms", lag.Milliseconds(),
	)
}

func (w *Worker) recordFailure(ctx context.Context, entry store.OutboxEntry, took time.Duration, cause error) {
	attempts := entry.Attempts + 1

	if attempts >= w.settings.MaxAttempts {
		w.recorder.DeliveryAttempt(entry.Topic, resultFailed, took)
		if err := w.store.MarkFailed(ctx, entry.ID, attempts, cause.Error(), w.now()); err != nil {
			w.logger.Error("mark entry failed", "event_id", entry.ID, "error", err)
			return
		}
		w.logger.Error("event delivery exhausted", "event_id", entry.ID, "topic", entry.Topic, "attempts", attempts, "error", cause)
		return
	}

	w.recorder.DeliveryAttempt(entry.Topic, resultRetry, took)
	delay := Backoff(attempts, w.settings.BaseDelay)
	if err := w.store.Reschedule(ctx, entry.ID, attempts, w.now().Add(delay), cause.Error()); err != nil {
		w.logger.Error("reschedule entry", "event_id", entry.ID, "error", err)
		return
	}
	w.logger.Warn("event delivery failed, will retry",
		"event_id", entry.ID,
		"topic", entry.Topic,
		"attempts", attempts,
		"retry_in_ms", delay.Milliseconds(),
		"error", cause,
	)
}
