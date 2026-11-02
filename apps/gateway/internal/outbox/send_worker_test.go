package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type fakeSendSource struct {
	batches [][]store.SendJob
	err     error
	calls   int
}

func (f *fakeSendSource) ListDueSends(context.Context, time.Time, int) ([]store.SendJob, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return batch, nil
}

type recordingProcessor struct {
	processed []string
	onProcess func(job store.SendJob)
}

func (r *recordingProcessor) Process(_ context.Context, job store.SendJob) {
	r.processed = append(r.processed, job.ID)
	if r.onProcess != nil {
		r.onProcess(job)
	}
}

func TestSendWorkerDrain(t *testing.T) {
	jobs := func(ids ...string) []store.SendJob {
		batch := make([]store.SendJob, 0, len(ids))
		for _, id := range ids {
			batch = append(batch, store.SendJob{ID: id})
		}
		return batch
	}

	tests := []struct {
		name          string
		batches       [][]store.SendJob
		batchSize     int
		listErr       error
		cancelAfter   string
		wantProcessed []string
		wantCalls     int
	}{
		{name: "nothing due", batchSize: 2, wantCalls: 1},
		{name: "partial batch ends the drain", batches: [][]store.SendJob{jobs("a")}, batchSize: 2, wantProcessed: []string{"a"}, wantCalls: 1},
		{name: "full batch triggers another pass", batches: [][]store.SendJob{jobs("a", "b"), jobs("c")}, batchSize: 2, wantProcessed: []string{"a", "b", "c"}, wantCalls: 2},
		{name: "listing failure stops the drain", listErr: errors.New("db down"), batchSize: 2, wantCalls: 1},
		{name: "shutdown mid batch stops before the next job", batches: [][]store.SendJob{jobs("a", "b", "c")}, batchSize: 3, cancelAfter: "a", wantProcessed: []string{"a"}, wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &fakeSendSource{batches: tt.batches, err: tt.listErr}
			processor := &recordingProcessor{onProcess: func(job store.SendJob) {
				if job.ID == tt.cancelAfter {
					cancel()
				}
			}}
			worker := NewSendWorker(source, processor, time.Second, tt.batchSize, slog.New(slog.NewTextHandler(io.Discard, nil)))

			worker.drain(ctx)

			if !slices.Equal(processor.processed, tt.wantProcessed) || source.calls != tt.wantCalls {
				t.Fatalf("processed %v in %d lists, want %v in %d", processor.processed, source.calls, tt.wantProcessed, tt.wantCalls)
			}
		})
	}
}

func TestSendWorkerRunStopsOnShutdown(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "run returns once the context is canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			source := &fakeSendSource{batches: [][]store.SendJob{{{ID: "a"}}}}
			processor := &recordingProcessor{}
			worker := NewSendWorker(source, processor, 10*time.Millisecond, 5, slog.New(slog.NewTextHandler(io.Discard, nil)))

			done := make(chan struct{})
			go func() {
				worker.Run(ctx)
				close(done)
			}()
			time.Sleep(50 * time.Millisecond)
			cancel()

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Run did not return after the context was canceled")
			}
			if !slices.Equal(processor.processed, []string{"a"}) {
				t.Errorf("processed %v, want [a]", processor.processed)
			}
		})
	}
}
