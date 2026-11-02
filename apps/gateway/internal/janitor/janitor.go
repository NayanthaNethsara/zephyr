package janitor

import (
	"context"
	"log/slog"
	"time"
)

type Task struct {
	Name string
	Run  func(ctx context.Context, now time.Time) error
}

type Janitor struct {
	tasks    []Task
	interval time.Duration
	logger   *slog.Logger
	now      func() time.Time
}

func New(tasks []Task, interval time.Duration, logger *slog.Logger) *Janitor {
	return &Janitor{
		tasks:    tasks,
		interval: interval,
		logger:   logger,
		now:      time.Now,
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
	for _, task := range j.tasks {
		if err := task.Run(ctx, now); err != nil && ctx.Err() == nil {
			j.logger.Error("cleanup task failed", "task", task.Name, "error", err)
		}
	}
}
