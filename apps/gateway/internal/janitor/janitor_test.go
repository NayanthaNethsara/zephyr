package janitor

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"
)

func TestSweep(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broken := errors.New("disk full")

	tests := []struct {
		name            string
		failing         map[string]error
		isCanceledFirst bool
		wantRan         []string
		wantTimes       []time.Time
		wantFailures    int
	}{
		{name: "every task runs in order with the same time", wantRan: []string{"rows", "sessions", "sends"}, wantTimes: []time.Time{now, now, now}},
		{name: "a failing task does not stop the others", failing: map[string]error{"sessions": broken}, wantRan: []string{"rows", "sessions", "sends"}, wantTimes: []time.Time{now, now, now}, wantFailures: 1},
		{name: "every task failing is still survivable", failing: map[string]error{"rows": broken, "sessions": broken, "sends": broken}, wantRan: []string{"rows", "sessions", "sends"}, wantTimes: []time.Time{now, now, now}, wantFailures: 3},
		{name: "failures during shutdown are not reported", failing: map[string]error{"rows": broken}, isCanceledFirst: true, wantRan: []string{"rows", "sessions", "sends"}, wantTimes: []time.Time{now, now, now}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran []string
			var times []time.Time
			var tasks []Task
			for _, name := range []string{"rows", "sessions", "sends"} {
				tasks = append(tasks, Task{Name: name, Run: func(_ context.Context, at time.Time) error {
					ran = append(ran, name)
					times = append(times, at)
					return tt.failing[name]
				}})
			}
			failures := 0
			logger := slog.New(slog.NewJSONHandler(countingWriter{count: &failures}, nil))
			janitor := New(tasks, time.Minute, logger)
			janitor.now = func() time.Time { return now }

			ctx, cancel := context.WithCancel(context.Background())
			if tt.isCanceledFirst {
				cancel()
			}
			defer cancel()
			janitor.sweep(ctx)

			if !slices.Equal(ran, tt.wantRan) || !slices.Equal(times, tt.wantTimes) {
				t.Errorf("ran %v at %v, want %v at %v", ran, times, tt.wantRan, tt.wantTimes)
			}
			if failures != tt.wantFailures {
				t.Errorf("logged %d failures, want %d", failures, tt.wantFailures)
			}
		})
	}
}

type countingWriter struct {
	count *int
}

func (c countingWriter) Write(p []byte) (int, error) {
	*c.count++
	return len(p), nil
}
