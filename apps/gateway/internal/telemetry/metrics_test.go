package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type fakeStats struct {
	stats store.OutboxStats
	err   error
}

func (f fakeStats) OutboxStats(context.Context) (store.OutboxStats, error) {
	return f.stats, f.err
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestRecordedMetricsAreExposed(t *testing.T) {
	tests := []struct {
		name   string
		record func(m *Metrics)
		want   []string
	}{
		{
			name: "webhook outcomes are counted by result",
			record: func(m *Metrics) {
				m.WebhookResult("accepted")
				m.WebhookResult("accepted")
				m.WebhookResult("invalid_signature")
			},
			want: []string{
				`zephyr_webhook_requests_total{result="accepted"} 2`,
				`zephyr_webhook_requests_total{result="invalid_signature"} 1`,
			},
		},
		{
			name: "ingested events are split into queued and duplicate",
			record: func(m *Metrics) {
				m.EventIngested("message.inbound", true)
				m.EventIngested("message.inbound", false)
			},
			want: []string{
				`zephyr_events_ingested_total{outcome="queued",type="message.inbound"} 1`,
				`zephyr_events_ingested_total{outcome="duplicate",type="message.inbound"} 1`,
			},
		},
		{
			name: "delivery attempts and lag are recorded",
			record: func(m *Metrics) {
				m.DeliveryAttempt("wa.inbound", "success", 40*time.Millisecond)
				m.DeliveryAttempt("wa.inbound", "retry", time.Second)
				m.EventDelivered("wa.inbound", 3*time.Second)
			},
			want: []string{
				`zephyr_delivery_attempts_total{result="success",topic="wa.inbound"} 1`,
				`zephyr_delivery_attempts_total{result="retry",topic="wa.inbound"} 1`,
				`zephyr_delivery_duration_seconds_count{result="retry",topic="wa.inbound"} 1`,
				`zephyr_delivery_lag_seconds_count{topic="wa.inbound"} 1`,
			},
		},
		{
			name: "http requests are counted by route and status",
			record: func(m *Metrics) {
				m.HTTPRequest("POST", "/webhook", 200, 5*time.Millisecond)
			},
			want: []string{
				`zephyr_http_requests_total{method="POST",route="/webhook",status="200"} 1`,
				`zephyr_http_request_duration_seconds_count{method="POST",route="/webhook"} 1`,
			},
		},
		{
			name:   "go runtime metrics are always present",
			record: func(*Metrics) {},
			want:   []string{"go_goroutines"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			tt.record(m)

			body := scrape(t, m)
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("scrape output missing %q", want)
				}
			}
		})
	}
}

func TestOutboxCollector(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		stats fakeStats
		want  []string
	}{
		{
			name:  "queue with backlog",
			stats: fakeStats{stats: store.OutboxStats{Pending: 3, Failed: 1, OldestPendingCreatedAt: now.Add(-90 * time.Second)}},
			want: []string{
				`zephyr_outbox_entries{state="pending"} 3`,
				`zephyr_outbox_entries{state="failed"} 1`,
				`zephyr_outbox_oldest_pending_age_seconds 90`,
				`zephyr_outbox_stats_error 0`,
			},
		},
		{
			name:  "empty queue",
			stats: fakeStats{},
			want: []string{
				`zephyr_outbox_entries{state="pending"} 0`,
				`zephyr_outbox_oldest_pending_age_seconds 0`,
			},
		},
		{
			name:  "store unavailable",
			stats: fakeStats{err: errors.New("db down")},
			want:  []string{`zephyr_outbox_stats_error 1`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			m.registry.MustRegister(newOutboxCollector(tt.stats, func() time.Time { return now }))

			body := scrape(t, m)
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("scrape output missing %q", want)
				}
			}
		})
	}
}
