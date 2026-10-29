package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const (
	requestTimeout   = 15 * time.Second
	maxResponseBytes = 1 << 20
)

type Client struct {
	baseURL     string
	apiVersion  string
	accessToken string
	httpClient  *http.Client
}

func NewClient(baseURL, apiVersion, accessToken string) *Client {
	return &Client{
		baseURL:     baseURL,
		apiVersion:  apiVersion,
		accessToken: accessToken,
		httpClient:  &http.Client{Timeout: requestTimeout},
	}
}

type sendResponse struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
}

type errorResponse struct {
	Error struct {
		Message   string `json:"message"`
		Code      int    `json:"code"`
		FBTraceID string `json:"fbtrace_id"`
		ErrorData struct {
			Details string `json:"details"`
		} `json:"error_data"`
	} `json:"error"`
}

func (c *Client) Send(ctx context.Context, phoneNumberID string, message core.OutboundMessage) (core.SendResult, error) {
	body, err := json.Marshal(map[string]any{
		"messaging_product":  "whatsapp",
		"recipient_type":     "individual",
		"to":                 message.To,
		"type":               string(message.Type),
		string(message.Type): message.Content,
	})
	if err != nil {
		return core.SendResult{}, fmt.Errorf("encode message: %w", err)
	}

	endpoint := fmt.Sprintf("%s/%s/%s/messages", c.baseURL, c.apiVersion, url.PathEscape(phoneNumberID))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return core.SendResult{}, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return core.SendResult{}, transportError(err)
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return core.SendResult{}, &core.SendError{Code: core.CodeUpstreamUnavailable, Message: "could not read the response from Meta", IsRetryable: true}
	}

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return core.SendResult{}, apiError(response.StatusCode, payload)
	}
	return parseSuccess(payload)
}

func parseSuccess(payload []byte) (core.SendResult, error) {
	var parsed sendResponse
	if err := json.Unmarshal(payload, &parsed); err != nil || len(parsed.Messages) == 0 || parsed.Messages[0].ID == "" {
		return core.SendResult{}, &core.SendError{Code: core.CodeUpstreamUnavailable, Message: "Meta returned an unexpected response", IsRetryable: true}
	}
	return core.SendResult{MessageID: parsed.Messages[0].ID}, nil
}

func apiError(httpStatus int, payload []byte) *core.SendError {
	var parsed errorResponse
	_ = json.Unmarshal(payload, &parsed)

	classified := classifyByStatus(httpStatus)
	if hasErrorObject(parsed) {
		classified = classifyError(parsed.Error.Code, httpStatus)
	}
	message := parsed.Error.ErrorData.Details
	if message == "" {
		message = parsed.Error.Message
	}
	if message == "" {
		message = fmt.Sprintf("Meta responded with status %d", httpStatus)
	}

	return &core.SendError{
		Code:        classified.code,
		Message:     message,
		IsRetryable: classified.isRetryable,
		MetaCode:    parsed.Error.Code,
		TraceID:     parsed.Error.FBTraceID,
	}
}

func hasErrorObject(parsed errorResponse) bool {
	return parsed.Error.Code != 0 || parsed.Error.Message != ""
}

func transportError(err error) *core.SendError {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &core.SendError{Code: core.CodeUpstreamTimeout, Message: "Meta did not respond in time", IsRetryable: true}
	}
	return &core.SendError{Code: core.CodeUpstreamUnavailable, Message: "could not reach Meta", IsRetryable: true}
}
