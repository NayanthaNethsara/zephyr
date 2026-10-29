package config

import (
	"strings"
	"testing"
	"time"
)

var configKeys = []string{
	"GATEWAY_PORT", "LOG_LEVEL", "DB_URL", "META_APP_SECRET", "META_VERIFY_TOKEN",
	"TRANSPORT", "TRANSPORT_TOPIC_PREFIX", "WEBHOOK_URL", "WEBHOOK_SIGNING_SECRET",
	"RETRY_MAX_ATTEMPTS", "RETRY_BASE_DELAY", "DEDUP_TTL", "FAILED_RETENTION",
	"META_API_VERSION", "WA_PHONE_NUMBER_ID", "WA_ACCESS_TOKEN", "API_KEYS", "IDEMPOTENCY_TTL",
}

func requiredEnv() map[string]string {
	return map[string]string{
		"META_APP_SECRET":        "app-secret",
		"META_VERIFY_TOKEN":      "verify-token-123456",
		"WEBHOOK_URL":            "https://example.com/hook",
		"WEBHOOK_SIGNING_SECRET": "signing-secret-123456",
	}
}

func withOverrides(overrides map[string]string) map[string]string {
	env := requiredEnv()
	for key, value := range overrides {
		env[key] = value
	}
	return env
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults with required values",
			env:  requiredEnv(),
			want: Config{
				Port:                 8080,
				LogLevel:             "info",
				DBURL:                "sqlite:///data/gateway.db",
				MetaAppSecret:        "app-secret",
				MetaVerifyToken:      "verify-token-123456",
				Transport:            "http",
				TransportTopicPrefix: "wa",
				WebhookURL:           "https://example.com/hook",
				WebhookSigningSecret: "signing-secret-123456",
				RetryMaxAttempts:     8,
				RetryBaseDelay:       2 * time.Second,
				DedupTTL:             48 * time.Hour,
				FailedRetention:      168 * time.Hour,
				MetaAPIVersion:       "v24.0",
				IdempotencyTTL:       24 * time.Hour,
			},
		},
		{
			name: "environment overrides defaults",
			env: withOverrides(map[string]string{
				"GATEWAY_PORT":       "9090",
				"LOG_LEVEL":          "debug",
				"RETRY_BASE_DELAY":   "500ms",
				"DB_URL":             "sqlite://./data/test.db",
				"RETRY_MAX_ATTEMPTS": "3",
			}),
			want: Config{
				Port:                 9090,
				LogLevel:             "debug",
				DBURL:                "sqlite://./data/test.db",
				MetaAppSecret:        "app-secret",
				MetaVerifyToken:      "verify-token-123456",
				Transport:            "http",
				TransportTopicPrefix: "wa",
				WebhookURL:           "https://example.com/hook",
				WebhookSigningSecret: "signing-secret-123456",
				RetryMaxAttempts:     3,
				RetryBaseDelay:       500 * time.Millisecond,
				DedupTTL:             48 * time.Hour,
				FailedRetention:      168 * time.Hour,
				MetaAPIVersion:       "v24.0",
				IdempotencyTTL:       24 * time.Hour,
			},
		},
		{
			name: "sending enabled with valid keys",
			env: withOverrides(map[string]string{
				"WA_PHONE_NUMBER_ID": "111",
				"WA_ACCESS_TOKEN":    "token",
				"API_KEYS":           strings.Repeat("a", 32) + ", " + strings.Repeat("b", 40),
				"META_API_VERSION":   "v25.1",
			}),
			want: Config{
				Port:                 8080,
				LogLevel:             "info",
				DBURL:                "sqlite:///data/gateway.db",
				MetaAppSecret:        "app-secret",
				MetaVerifyToken:      "verify-token-123456",
				Transport:            "http",
				TransportTopicPrefix: "wa",
				WebhookURL:           "https://example.com/hook",
				WebhookSigningSecret: "signing-secret-123456",
				RetryMaxAttempts:     8,
				RetryBaseDelay:       2 * time.Second,
				DedupTTL:             48 * time.Hour,
				FailedRetention:      168 * time.Hour,
				MetaAPIVersion:       "v25.1",
				IdempotencyTTL:       24 * time.Hour,
				WAPhoneNumberID:      "111",
				WAAccessToken:        "token",
				APIKeys:              strings.Repeat("a", 32) + ", " + strings.Repeat("b", 40),
			},
		},
		{name: "phone number without token", env: withOverrides(map[string]string{"WA_PHONE_NUMBER_ID": "111"}), wantErr: true},
		{name: "token without phone number", env: withOverrides(map[string]string{"WA_ACCESS_TOKEN": "token"}), wantErr: true},
		{name: "sending enabled without api keys", env: withOverrides(map[string]string{"WA_PHONE_NUMBER_ID": "111", "WA_ACCESS_TOKEN": "token"}), wantErr: true},
		{name: "sending enabled with a short api key", env: withOverrides(map[string]string{"WA_PHONE_NUMBER_ID": "111", "WA_ACCESS_TOKEN": "token", "API_KEYS": "short"}), wantErr: true},
		{name: "idempotency ttl override is accepted", env: withOverrides(map[string]string{"IDEMPOTENCY_TTL": "1h"}), want: Config{
			Port: 8080, LogLevel: "info", DBURL: "sqlite:///data/gateway.db", MetaAppSecret: "app-secret",
			MetaVerifyToken: "verify-token-123456", Transport: "http", TransportTopicPrefix: "wa",
			WebhookURL: "https://example.com/hook", WebhookSigningSecret: "signing-secret-123456",
			RetryMaxAttempts: 8, RetryBaseDelay: 2 * time.Second, DedupTTL: 48 * time.Hour,
			FailedRetention: 168 * time.Hour, MetaAPIVersion: "v24.0", IdempotencyTTL: time.Hour,
		}},
		{name: "zero idempotency ttl", env: withOverrides(map[string]string{"IDEMPOTENCY_TTL": "0s"}), wantErr: true},
		{name: "malformed api version", env: withOverrides(map[string]string{"META_API_VERSION": "24"}), wantErr: true},
		{name: "missing required values", env: map[string]string{}, wantErr: true},
		{name: "short verify token", env: withOverrides(map[string]string{"META_VERIFY_TOKEN": "short"}), wantErr: true},
		{name: "short signing secret", env: withOverrides(map[string]string{"WEBHOOK_SIGNING_SECRET": "short"}), wantErr: true},
		{name: "port out of range", env: withOverrides(map[string]string{"GATEWAY_PORT": "70000"}), wantErr: true},
		{name: "unknown log level", env: withOverrides(map[string]string{"LOG_LEVEL": "verbose"}), wantErr: true},
		{name: "unsupported database", env: withOverrides(map[string]string{"DB_URL": "postgres://localhost/db"}), wantErr: true},
		{name: "unsupported transport", env: withOverrides(map[string]string{"TRANSPORT": "nats"}), wantErr: true},
		{name: "invalid webhook url", env: withOverrides(map[string]string{"WEBHOOK_URL": "not a url"}), wantErr: true},
		{name: "zero retry attempts", env: withOverrides(map[string]string{"RETRY_MAX_ATTEMPTS": "0"}), wantErr: true},
		{name: "negative dedup ttl", env: withOverrides(map[string]string{"DEDUP_TTL": "-1h"}), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range configKeys {
				t.Setenv(key, "")
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			got, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSQLitePath(t *testing.T) {
	tests := []struct {
		name  string
		dbURL string
		want  string
	}{
		{name: "absolute path", dbURL: "sqlite:///data/gateway.db", want: "/data/gateway.db"},
		{name: "relative path", dbURL: "sqlite://./data/gateway.db", want: "./data/gateway.db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Config{DBURL: tt.dbURL}).SQLitePath(); got != tt.want {
				t.Errorf("SQLitePath(%q) = %q, want %q", tt.dbURL, got, tt.want)
			}
		})
	}
}

func TestIsAPIVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{name: "current style", version: "v24.0", want: true},
		{name: "two digit minor", version: "v19.10", want: true},
		{name: "missing prefix", version: "24.0", want: false},
		{name: "missing minor", version: "v24", want: false},
		{name: "letters", version: "vx.y", want: false},
		{name: "empty", version: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAPIVersion(tt.version); got != tt.want {
				t.Errorf("isAPIVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestAPIKeyList(t *testing.T) {
	tests := []struct {
		name string
		keys string
		want []string
	}{
		{name: "empty", keys: "", want: nil},
		{name: "single key", keys: "a", want: []string{"a"}},
		{name: "trims whitespace and skips blanks", keys: " a , ,b,", want: []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (Config{APIKeys: tt.keys}).APIKeyList()
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("APIKeyList() = %v, want %v", got, tt.want)
			}
		})
	}
}
