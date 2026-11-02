package nats

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/transport"
)

const waitForDelivery = 3 * time.Second

func startServer(t *testing.T) string {
	t.Helper()
	broker, err := server.NewServer(&server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go broker.Start()
	if !broker.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats server did not become ready")
	}
	t.Cleanup(broker.Shutdown)
	return broker.ClientURL()
}

func openTransport(t *testing.T, url, prefix string) *Transport {
	t.Helper()
	opened, err := New(context.Background(), url, prefix)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return opened
}

type delivery struct {
	id   string
	data string
}

func subscribe(ctx context.Context, t *testing.T, opened *Transport, topic string, respond func(transport.Message, int)) chan delivery {
	t.Helper()
	deliveries := make(chan delivery, 16)
	var count atomic.Int32
	err := opened.Subscribe(ctx, topic, func(_ context.Context, received transport.Message) {
		deliveries <- delivery{id: received.ID(), data: string(received.Data())}
		respond(received, int(count.Add(1)))
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	return deliveries
}

func ackAll(received transport.Message, _ int) { _ = received.Ack() }

func expectDelivery(t *testing.T, deliveries chan delivery) delivery {
	t.Helper()
	select {
	case got := <-deliveries:
		return got
	case <-time.After(waitForDelivery):
		t.Fatal("no delivery arrived in time")
		return delivery{}
	}
}

func expectNoDelivery(t *testing.T, deliveries chan delivery) {
	t.Helper()
	select {
	case got := <-deliveries:
		t.Fatalf("unexpected delivery %+v", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestNames(t *testing.T) {
	tests := []struct {
		name        string
		prefix      string
		topic       string
		wantStream  string
		wantDurable string
	}{
		{name: "default prefix", prefix: "wa", topic: "wa.outbound", wantStream: "WA", wantDurable: "zephyr_wa_outbound"},
		{name: "dots in the prefix", prefix: "my.app", topic: "my.app.outbound", wantStream: "MY_APP", wantDurable: "zephyr_my_app_outbound"},
		{name: "spaces are replaced", prefix: "my app", topic: "my app.outbound", wantStream: "MY_APP", wantDurable: "zephyr_my_app_outbound"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := streamName(tt.prefix); got != tt.wantStream {
				t.Errorf("streamName() = %q, want %q", got, tt.wantStream)
			}
			if got := durableName(tt.topic); got != tt.wantDurable {
				t.Errorf("durableName() = %q, want %q", got, tt.wantDurable)
			}
		})
	}
}

func TestEventID(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "envelope with an id", payload: `{"id":"evt_1","type":"message.inbound"}`, want: "evt_1"},
		{name: "envelope without an id", payload: `{"type":"message.inbound"}`, want: ""},
		{name: "id of the wrong type", payload: `{"id":5}`, want: ""},
		{name: "not json", payload: `not json`, want: ""},
		{name: "empty payload", payload: ``, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eventID([]byte(tt.payload)); got != tt.want {
				t.Errorf("eventID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewPreparesTheStream(t *testing.T) {
	tests := []struct {
		name     string
		prefixes []string
	}{
		{name: "opening twice with the same prefix is fine", prefixes: []string{"wa", "wa"}},
		{name: "different prefixes get their own streams", prefixes: []string{"wa", "other"}},
		{name: "prefix with a dot", prefixes: []string{"my.app"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := startServer(t)
			for _, prefix := range tt.prefixes {
				opened := openTransport(t, url, prefix)
				if opened.Name() != "nats" {
					t.Errorf("Name() = %q, want nats", opened.Name())
				}
				if err := opened.Publish(context.Background(), prefix+".inbound", "94770000000", []byte(`{"id":"evt_x"}`)); err != nil {
					t.Errorf("Publish() on %s error = %v", prefix, err)
				}
			}
		})
	}
}

func TestPublishAndSubscribe(t *testing.T) {
	tests := []struct {
		name          string
		subscribeTo   string
		publishTo     string
		payload       string
		wantDelivered bool
	}{
		{name: "message on the subscribed topic arrives intact", subscribeTo: "wa.outbound", publishTo: "wa.outbound", payload: `{"to":"94770000000","type":"text"}`, wantDelivered: true},
		{name: "message on another topic is not delivered", subscribeTo: "wa.outbound", publishTo: "wa.inbound", payload: `{"id":"evt_1"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opened := openTransport(t, startServer(t), "wa")
			deliveries := subscribe(ctx, t, opened, tt.subscribeTo, ackAll)

			if err := opened.Publish(ctx, tt.publishTo, "94770000000", []byte(tt.payload)); err != nil {
				t.Fatal(err)
			}

			if !tt.wantDelivered {
				expectNoDelivery(t, deliveries)
				return
			}
			got := expectDelivery(t, deliveries)
			if got.data != tt.payload || !strings.HasPrefix(got.id, "nats:") {
				t.Errorf("delivery = %+v, want payload %s and a nats id", got, tt.payload)
			}
		})
	}
}

func TestPublishDeduplicatesByEventID(t *testing.T) {
	tests := []struct {
		name      string
		payloads  []string
		wantCount int
	}{
		{name: "same event id is delivered once", payloads: []string{`{"id":"evt_1"}`, `{"id":"evt_1"}`}, wantCount: 1},
		{name: "different event ids are both delivered", payloads: []string{`{"id":"evt_1"}`, `{"id":"evt_2"}`}, wantCount: 2},
		{name: "payloads without an id are never deduplicated", payloads: []string{`{"to":"1"}`, `{"to":"1"}`}, wantCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opened := openTransport(t, startServer(t), "wa")
			deliveries := subscribe(ctx, t, opened, "wa.inbound", ackAll)

			for _, payload := range tt.payloads {
				if err := opened.Publish(ctx, "wa.inbound", "", []byte(payload)); err != nil {
					t.Fatal(err)
				}
			}

			for range tt.wantCount {
				expectDelivery(t, deliveries)
			}
			expectNoDelivery(t, deliveries)
		})
	}
}

func TestNackRedeliversAfterTheDelay(t *testing.T) {
	tests := []struct {
		name         string
		nackFirst    int
		delay        time.Duration
		wantAttempts int
	}{
		{name: "one nack means a second attempt", nackFirst: 1, delay: 50 * time.Millisecond, wantAttempts: 2},
		{name: "two nacks mean a third attempt", nackFirst: 2, delay: 50 * time.Millisecond, wantAttempts: 3},
		{name: "ack right away means a single attempt", nackFirst: 0, delay: 50 * time.Millisecond, wantAttempts: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opened := openTransport(t, startServer(t), "wa")
			deliveries := subscribe(ctx, t, opened, "wa.outbound", func(received transport.Message, attempt int) {
				if attempt <= tt.nackFirst {
					_ = received.Nack(tt.delay)
					return
				}
				_ = received.Ack()
			})

			if err := opened.Publish(ctx, "wa.outbound", "", []byte(`{"to":"94770000000"}`)); err != nil {
				t.Fatal(err)
			}

			started := time.Now()
			ids := map[string]bool{}
			for range tt.wantAttempts {
				ids[expectDelivery(t, deliveries).id] = true
			}
			expectNoDelivery(t, deliveries)

			if len(ids) != 1 {
				t.Errorf("redeliveries carried %d different ids, want the same one", len(ids))
			}
			if minimum := time.Duration(tt.nackFirst) * tt.delay; time.Since(started) < minimum-10*time.Millisecond {
				t.Errorf("redelivered after %v, want at least %v", time.Since(started), minimum)
			}
		})
	}
}

func TestSubscribeStopsWithItsContext(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "nothing is delivered after cancellation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			opened := openTransport(t, startServer(t), "wa")
			deliveries := subscribe(ctx, t, opened, "wa.outbound", ackAll)
			if err := opened.Publish(context.Background(), "wa.outbound", "", []byte(`{"n":1}`)); err != nil {
				t.Fatal(err)
			}
			expectDelivery(t, deliveries)

			cancel()
			time.Sleep(200 * time.Millisecond)
			if err := opened.Publish(context.Background(), "wa.outbound", "", []byte(`{"n":2}`)); err != nil {
				t.Fatal(err)
			}

			expectNoDelivery(t, deliveries)
		})
	}
}

func TestFailures(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{name: "connecting to nothing fails", run: func(t *testing.T) error {
			_, err := New(context.Background(), "nats://127.0.0.1:1", "wa")
			return err
		}},
		{name: "publishing after close fails", run: func(t *testing.T) error {
			opened, err := New(context.Background(), startServer(t), "wa")
			if err != nil {
				t.Fatal(err)
			}
			_ = opened.Close()
			time.Sleep(200 * time.Millisecond)
			return opened.Publish(context.Background(), "wa.inbound", "", []byte(`{"id":"evt_1"}`))
		}},
		{name: "subscribing after close fails", run: func(t *testing.T) error {
			opened, err := New(context.Background(), startServer(t), "wa")
			if err != nil {
				t.Fatal(err)
			}
			_ = opened.Close()
			time.Sleep(200 * time.Millisecond)
			return opened.Subscribe(context.Background(), "wa.outbound", func(context.Context, transport.Message) {})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(t); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
