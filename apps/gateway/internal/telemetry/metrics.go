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
	sendRequests     *prometheus.CounterVec
	sendDuration     *prometheus.HistogramVec
	mediaRequests    *prometheus.CounterVec
	mediaDuration    *prometheus.HistogramVec
	mediaBytes       *prometheus.CounterVec
	eventReplays     *prometheus.CounterVec
	asyncSends       *prometheus.CounterVec
	consumed         *prometheus.CounterVec
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
		sendRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "send_requests_total",
			Help:      "Message send requests by message type and result (sent or an error code).",
		}, []string{"type", "result"}),
		sendDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "send_duration_seconds",
			Help:      "Time to handle a send request, including the call to Meta.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"type"}),
		mediaRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "media_requests_total",
			Help:      "Media upload and download requests by operation and result (ok, interrupted, or an error code).",
		}, []string{"operation", "result"}),
		mediaDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "media_duration_seconds",
			Help:      "Time to complete a media transfer.",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
		}, []string{"operation"}),
		mediaBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "media_bytes_total",
			Help:      "Bytes streamed through media uploads and downloads.",
		}, []string{"operation"}),
		asyncSends: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "async_sends_total",
			Help:      "Attempts to deliver queued messages to Meta, by result (sent, retry, or failed).",
		}, []string{"result"}),
		consumed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbound_consumed_total",
			Help:      "Send requests read from the broker, by result (queued, rejected, or retry).",
		}, []string{"result"}),
		eventReplays: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "event_replays_total",
			Help:      "Requests to replay a failed event, by result (queued or an error code).",
		}, []string{"result"}),
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
		m.sendRequests,
		m.sendDuration,
		m.mediaRequests,
		m.mediaDuration,
		m.mediaBytes,
		m.eventReplays,
		m.asyncSends,
		m.consumed,
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

func (m *Metrics) SendResult(messageType, result string, took time.Duration) {
	m.sendRequests.WithLabelValues(messageType, result).Inc()
	m.sendDuration.WithLabelValues(messageType).Observe(took.Seconds())
}

func (m *Metrics) OutboundConsumed(result string) {
	m.consumed.WithLabelValues(result).Inc()
}

func (m *Metrics) AsyncSendResult(result string) {
	m.asyncSends.WithLabelValues(result).Inc()
}

func (m *Metrics) ReplayResult(result string) {
	m.eventReplays.WithLabelValues(result).Inc()
}

func (m *Metrics) MediaResult(operation, result string, size int64, took time.Duration) {
	m.mediaRequests.WithLabelValues(operation, result).Inc()
	m.mediaDuration.WithLabelValues(operation).Observe(took.Seconds())
	if size > 0 {
		m.mediaBytes.WithLabelValues(operation).Add(float64(size))
	}
}
