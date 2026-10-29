package telemetry

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

const statsTimeout = 2 * time.Second

type OutboxStatsReader interface {
	OutboxStats(ctx context.Context) (store.OutboxStats, error)
}

type outboxCollector struct {
	source     OutboxStatsReader
	now        func() time.Time
	entries    *prometheus.Desc
	oldestAge  *prometheus.Desc
	scrapeFail *prometheus.Desc
}

func (m *Metrics) WatchOutbox(source OutboxStatsReader) {
	m.registry.MustRegister(newOutboxCollector(source, time.Now))
}

func newOutboxCollector(source OutboxStatsReader, now func() time.Time) *outboxCollector {
	return &outboxCollector{
		source: source,
		now:    now,
		entries: prometheus.NewDesc(
			namespace+"_outbox_entries",
			"Outbox entries by state.",
			[]string{"state"}, nil,
		),
		oldestAge: prometheus.NewDesc(
			namespace+"_outbox_oldest_pending_age_seconds",
			"Age of the oldest pending outbox entry, 0 when the queue is empty.",
			nil, nil,
		),
		scrapeFail: prometheus.NewDesc(
			namespace+"_outbox_stats_error",
			"1 when the outbox statistics could not be read during the scrape.",
			nil, nil,
		),
	}
}

func (c *outboxCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.entries
	ch <- c.oldestAge
	ch <- c.scrapeFail
}

func (c *outboxCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()

	stats, err := c.source.OutboxStats(ctx)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.scrapeFail, prometheus.GaugeValue, 1)
		return
	}

	var oldestAge float64
	if !stats.OldestPendingCreatedAt.IsZero() {
		oldestAge = c.now().Sub(stats.OldestPendingCreatedAt).Seconds()
	}

	ch <- prometheus.MustNewConstMetric(c.entries, prometheus.GaugeValue, float64(stats.Pending), store.StatePending)
	ch <- prometheus.MustNewConstMetric(c.entries, prometheus.GaugeValue, float64(stats.Failed), store.StateFailed)
	ch <- prometheus.MustNewConstMetric(c.oldestAge, prometheus.GaugeValue, oldestAge)
	ch <- prometheus.MustNewConstMetric(c.scrapeFail, prometheus.GaugeValue, 0)
}
