package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type SendJobSource interface {
	ListDueSends(ctx context.Context, now time.Time, limit int) ([]store.SendJob, error)
}

type SendProcessor interface {
	Process(ctx context.Context, job store.SendJob)
}

type SendWorker struct {
	source       SendJobSource
	processor    SendProcessor
	pollInterval time.Duration
	batchSize    int
	logger       *slog.Logger
	now          func() time.Time
}

func NewSendWorker(source SendJobSource, processor SendProcessor, pollInterval time.Duration, batchSize int, logger *slog.Logger) *SendWorker {
	return &SendWorker{
		source:       source,
		processor:    processor,
		pollInterval: pollInterval,
		batchSize:    batchSize,
		logger:       logger,
		now:          time.Now,
	}
}

func (w *SendWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
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

func (w *SendWorker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		jobs, err := w.source.ListDueSends(ctx, w.now(), w.batchSize)
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Error("list due send jobs", "error", err)
			}
			return
		}

		for _, job := range jobs {
			if ctx.Err() != nil {
				return
			}
			w.processor.Process(ctx, job)
		}

		if len(jobs) < w.batchSize {
			return
		}
	}
}
