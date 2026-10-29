package transport

import "context"

type Transport interface {
	Name() string
	Publish(ctx context.Context, topic string, key string, payload []byte) error
	Close() error
}
