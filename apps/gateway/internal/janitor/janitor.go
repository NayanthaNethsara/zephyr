package janitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type Janitor struct {
	store           store.Store
	interval        time.Duration
	failedRetention time.Duration
	logger          *slog.Logger
	now             func() time.Time
}

func New(s store.Store, interval, failedRetention time.Duration, logger *slog.Logger) *Janitor {
	return &Janitor{
		store:           s,
		interval:        interval,
		failedRetention: failedRetention,
		logger:          logger,
		now:             time.Now,
	}
}

func (j *Janitor) Run(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.sweep(ctx)
		}
	}
}

func (j *Janitor) sweep(ctx context.Context) {
	now := j.now()
	if err := j.store.DeleteExpired(ctx, now, now.Add(-j.failedRetention)); err != nil && ctx.Err() == nil {
		j.logger.Error("delete expired rows", "error", err)
	}
}
