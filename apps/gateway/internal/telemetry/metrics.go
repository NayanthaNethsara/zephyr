package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "zephyr"

type Metrics struct {
	registry         *prometheus.Registry
	webhookRequests  *prometheus.CounterVec
	eventsIngested   *prometheus.CounterVec
	deliveryAttempts *prometheus.CounterVec
	deliveryDuration *prometheus.HistogramVec
	deliveryLag      *prometheus.HistogramVec
	httpRequests     *prometheus.CounterVec
	httpDuration     *prometheus.HistogramVec
}

func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		webhookRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "webhook_requests_total",
			Help:      "Meta webhook requests by outcome.",
		}, []string{"result"}),
		eventsIngested: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "events_ingested_total",
			Help:      "Events parsed from Meta webhooks, by type and whether they were queued or dropped as duplicates.",
		}, []string{"type", "outcome"}),
		deliveryAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "delivery_attempts_total",
			Help:      "Downstream delivery attempts by topic and result.",
		}, []string{"topic", "result"}),
		deliveryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "delivery_duration_seconds",
			Help:      "Duration of a single downstream delivery attempt.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"topic", "result"}),
		deliveryLag: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "delivery_lag_seconds",
			Help:      "Time from an event being accepted to it being delivered downstream.",
			Buckets:   []float64{0.05, 0.1, 0.5, 1, 2, 5, 10, 30, 60, 300, 900},
		}, []string{"topic"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_requests_total",
			Help:      "HTTP requests by method, route and status.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration by method and route.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.webhookRequests,
		m.eventsIngested,
		m.deliveryAttempts,
		m.deliveryDuration,
		m.deliveryLag,
		m.httpRequests,
		m.httpDuration,
	)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) WebhookResult(result string) {
	m.webhookRequests.WithLabelValues(result).Inc()
}

func (m *Metrics) EventIngested(eventType string, isQueued bool) {
	outcome := "duplicate"
	if isQueued {
		outcome = "queued"
	}
	m.eventsIngested.WithLabelValues(eventType, outcome).Inc()
}

func (m *Metrics) DeliveryAttempt(topic, result string, took time.Duration) {
	m.deliveryAttempts.WithLabelValues(topic, result).Inc()
	m.deliveryDuration.WithLabelValues(topic, result).Observe(took.Seconds())
}

func (m *Metrics) EventDelivered(topic string, lag time.Duration) {
	m.deliveryLag.WithLabelValues(topic).Observe(lag.Seconds())
}

func (m *Metrics) HTTPRequest(method, route string, status int, took time.Duration) {
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(took.Seconds())
}
