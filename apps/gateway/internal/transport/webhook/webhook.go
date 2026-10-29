package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/crypto"
)

const (
	eventHeader     = "X-Gateway-Event"
	deliveryHeader  = "X-Gateway-Delivery"
	signatureHeader = "X-Gateway-Signature"
	timestampHeader = "X-Gateway-Timestamp"
	signaturePrefix = "sha256="
)

type Transport struct {
	url    string
	secret []byte
	client *http.Client
	now    func() time.Time
}

func New(targetURL, signingSecret string) *Transport {
	return &Transport{
		url:    targetURL,
		secret: []byte(signingSecret),
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

func (t *Transport) Name() string {
	return "http"
}

func (t *Transport) Publish(ctx context.Context, _ string, _ string, payload []byte) error {
	var envelope struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	timestamp := strconv.FormatInt(t.now().Unix(), 10)
	signature := crypto.SignHMACSHA256(t.secret, []byte(timestamp), []byte("."), payload)

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(eventHeader, envelope.Type)
	request.Header.Set(deliveryHeader, envelope.ID)
	request.Header.Set(timestampHeader, timestamp)
	request.Header.Set(signatureHeader, signaturePrefix+signature)

	response, err := t.client.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("deliver webhook: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("webhook responded with status %d", response.StatusCode)
	}
	return nil
}

func (t *Transport) Close() error {
	t.client.CloseIdleConnections()
	return nil
}
