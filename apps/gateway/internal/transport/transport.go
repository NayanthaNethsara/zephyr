package transport

import (
	"context"
	"time"
)

type Transport interface {
	Name() string
	Publish(ctx context.Context, topic string, key string, payload []byte) error
	Close() error
}

type Message interface {
	ID() string
	Data() []byte
	Ack() error
	Nack(delay time.Duration) error
}

type Handler func(ctx context.Context, message Message)

type Subscriber interface {
	Subscribe(ctx context.Context, topic string, handler Handler) error
}
