package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/transport"
)

const (
	connectTimeout = 5 * time.Second
	streamMaxAge   = 7 * 24 * time.Hour
	ackWait        = 30 * time.Second
	maxDeliveries  = 10
	messageIDKey   = "Nats-Msg-Id"
	recipientKey   = "Zephyr-Key"
)

type Transport struct {
	conn   *natsgo.Conn
	stream jetstream.Stream
	js     jetstream.JetStream
}

func New(ctx context.Context, url, topicPrefix string) (*Transport, error) {
	conn, err := natsgo.Connect(url, natsgo.Name("zephyr"), natsgo.Timeout(connectTimeout), natsgo.MaxReconnects(-1))
	if err != nil {
		return nil, fmt.Errorf("connect to nats: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open jetstream: %w", err)
	}
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamName(topicPrefix),
		Subjects:  []string{topicPrefix + ".>"},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    streamMaxAge,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("prepare jetstream stream: %w", err)
	}
	return &Transport{conn: conn, stream: stream, js: js}, nil
}

func (t *Transport) Name() string {
	return "nats"
}

func (t *Transport) Publish(ctx context.Context, topic string, key string, payload []byte) error {
	message := &natsgo.Msg{Subject: topic, Data: payload, Header: natsgo.Header{}}
	if id := eventID(payload); id != "" {
		message.Header.Set(messageIDKey, id)
	}
	if key != "" {
		message.Header.Set(recipientKey, key)
	}
	if _, err := t.js.PublishMsg(ctx, message); err != nil {
		return fmt.Errorf("publish to nats: %w", err)
	}
	return nil
}

func (t *Transport) Subscribe(ctx context.Context, topic string, handler transport.Handler) error {
	consumer, err := t.stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       durableName(topic),
		FilterSubject: topic,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxDeliver:    maxDeliveries,
	})
	if err != nil {
		return fmt.Errorf("create nats consumer: %w", err)
	}
	consumption, err := consumer.Consume(func(received jetstream.Msg) {
		handler(ctx, message{received})
	})
	if err != nil {
		return fmt.Errorf("start nats consumer: %w", err)
	}
	go func() {
		<-ctx.Done()
		consumption.Stop()
	}()
	return nil
}

func (t *Transport) Close() error {
	return t.conn.Drain()
}

type message struct {
	received jetstream.Msg
}

func (m message) ID() string {
	metadata, err := m.received.Metadata()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("nats:%d", metadata.Sequence.Stream)
}

func (m message) Data() []byte {
	return m.received.Data()
}

func (m message) Ack() error {
	return m.received.Ack()
}

func (m message) Nack(delay time.Duration) error {
	return m.received.NakWithDelay(delay)
}

func streamName(topicPrefix string) string {
	return strings.ToUpper(strings.NewReplacer(".", "_", " ", "_").Replace(topicPrefix))
}

func durableName(topic string) string {
	return "zephyr_" + strings.NewReplacer(".", "_", " ", "_").Replace(topic)
}

func eventID(payload []byte) string {
	var envelope struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	return envelope.ID
}
