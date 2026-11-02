package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
)

const (
	sqliteScheme    = "sqlite://"
	minSecretLength = 16
	minAPIKeyLength = 32
)

type Config struct {
	Port                 int           `koanf:"gateway_port"`
	LogLevel             string        `koanf:"log_level"`
	DBURL                string        `koanf:"db_url"`
	MetaAppSecret        string        `koanf:"meta_app_secret"`
	MetaVerifyToken      string        `koanf:"meta_verify_token"`
	Transport            string        `koanf:"transport"`
	TransportTopicPrefix string        `koanf:"transport_topic_prefix"`
	WebhookURL           string        `koanf:"webhook_url"`
	WebhookSigningSecret string        `koanf:"webhook_signing_secret"`
	NATSURL              string        `koanf:"nats_url"`
	PhoneHashKey         string        `koanf:"phone_hash_key"`
	RetryMaxAttempts     int           `koanf:"retry_max_attempts"`
	RetryBaseDelay       time.Duration `koanf:"retry_base_delay"`
	DedupTTL             time.Duration `koanf:"dedup_ttl"`
	FailedRetention      time.Duration `koanf:"failed_retention"`
	MetaAPIVersion       string        `koanf:"meta_api_version"`
	WAPhoneNumberID      string        `koanf:"wa_phone_number_id"`
	WAAccessToken        string        `koanf:"wa_access_token"`
	APIKeys              string        `koanf:"api_keys"`
	IdempotencyTTL       time.Duration `koanf:"idempotency_ttl"`
	RateLimitMPS         int           `koanf:"rate_limit_mps"`
}

func defaultValues() map[string]any {
	return map[string]any{
		"gateway_port":           8080,
		"log_level":              "info",
		"db_url":                 "sqlite:///data/gateway.db",
		"transport":              "http",
		"transport_topic_prefix": "wa",
		"retry_max_attempts":     8,
		"retry_base_delay":       "2s",
		"dedup_ttl":              "48h",
		"failed_retention":       "168h",
		"meta_api_version":       "v24.0",
		"idempotency_ttl":        "24h",
		"rate_limit_mps":         80,
	}
}

func Load() (Config, error) {
	k := koanf.New(".")

	if err := k.Load(confmap.Provider(defaultValues(), "."), nil); err != nil {
		return Config{}, fmt.Errorf("load defaults: %w", err)
	}

	envProvider := env.Provider(".", env.Opt{
		TransformFunc: func(key, value string) (string, any) {
			if value == "" {
				return "", nil
			}
			return strings.ToLower(key), value
		},
	})
	if err := k.Load(envProvider, nil); err != nil {
		return Config{}, fmt.Errorf("load environment: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	return cfg, cfg.validate()
}

func (c Config) SQLitePath() string {
	return strings.TrimPrefix(c.DBURL, sqliteScheme)
}

func (c Config) SendingEnabled() bool {
	return c.WAPhoneNumberID != "" && c.WAAccessToken != ""
}

func (c Config) APIKeyList() []string {
	var keys []string
	for _, key := range strings.Split(c.APIKeys, ",") {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	return keys
}

func (c Config) validate() error {
	checks := []func() error{
		c.validatePort,
		c.validateLogLevel,
		c.validateMeta,
		c.validatePhoneHashKey,
		c.validateStorage,
		c.validateTransport,
		c.validateRetry,
		c.validateRateLimit,
		c.validateSending,
		c.validateAPIKeys,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validatePort() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("GATEWAY_PORT must be between 1 and 65535, got %d", c.Port)
	}
	return nil
}

func (c Config) validateLogLevel() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
		return nil
	}
	return fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error, got %q", c.LogLevel)
}

func (c Config) validateMeta() error {
	if c.MetaAppSecret == "" {
		return errors.New("META_APP_SECRET is required")
	}
	if len(c.MetaVerifyToken) < minSecretLength {
		return fmt.Errorf("META_VERIFY_TOKEN must be at least %d characters", minSecretLength)
	}
	return nil
}

func (c Config) validatePhoneHashKey() error {
	if len(c.PhoneHashKey) < minAPIKeyLength {
		return fmt.Errorf("PHONE_HASH_KEY must be at least %d characters, generate one with openssl rand -hex 32", minAPIKeyLength)
	}
	return nil
}

func (c Config) validateStorage() error {
	if !strings.HasPrefix(c.DBURL, sqliteScheme) || c.SQLitePath() == "" {
		return fmt.Errorf("DB_URL must be a sqlite:// URL, got %q", c.DBURL)
	}
	return nil
}

func (c Config) validateTransport() error {
	if c.TransportTopicPrefix == "" {
		return errors.New("TRANSPORT_TOPIC_PREFIX must not be empty")
	}
	switch c.Transport {
	case "http":
		return c.validateWebhookTarget()
	case "nats":
		return c.validateNATS()
	default:
		return fmt.Errorf("TRANSPORT %q is not supported, use http or nats", c.Transport)
	}
}

func (c Config) validateNATS() error {
	target, err := url.Parse(c.NATSURL)
	if err != nil || (target.Scheme != "nats" && target.Scheme != "tls") || target.Host == "" {
		return fmt.Errorf("NATS_URL must be a nats:// or tls:// URL when TRANSPORT is nats, got %q", c.NATSURL)
	}
	return nil
}

func (c Config) validateWebhookTarget() error {
	target, err := url.Parse(c.WebhookURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return fmt.Errorf("WEBHOOK_URL must be an http or https URL, got %q", c.WebhookURL)
	}
	if len(c.WebhookSigningSecret) < minSecretLength {
		return fmt.Errorf("WEBHOOK_SIGNING_SECRET must be at least %d characters", minSecretLength)
	}
	return nil
}

func (c Config) validateRetry() error {
	if c.RetryMaxAttempts < 1 {
		return fmt.Errorf("RETRY_MAX_ATTEMPTS must be at least 1, got %d", c.RetryMaxAttempts)
	}
	for name, value := range map[string]time.Duration{
		"RETRY_BASE_DELAY": c.RetryBaseDelay,
		"DEDUP_TTL":        c.DedupTTL,
		"FAILED_RETENTION": c.FailedRetention,
		"IDEMPOTENCY_TTL":  c.IdempotencyTTL,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be greater than zero", name)
		}
	}
	return nil
}

func (c Config) validateRateLimit() error {
	if c.RateLimitMPS < 1 {
		return fmt.Errorf("RATE_LIMIT_MPS must be at least 1, got %d", c.RateLimitMPS)
	}
	return nil
}

func (c Config) validateSending() error {
	if (c.WAPhoneNumberID == "") != (c.WAAccessToken == "") {
		return errors.New("WA_PHONE_NUMBER_ID and WA_ACCESS_TOKEN must be set together")
	}
	if !isAPIVersion(c.MetaAPIVersion) {
		return fmt.Errorf("META_API_VERSION must look like v24.0, got %q", c.MetaAPIVersion)
	}
	return nil
}

func (c Config) validateAPIKeys() error {
	keys := c.APIKeyList()
	if c.SendingEnabled() && len(keys) == 0 {
		return errors.New("API_KEYS is required when sending is enabled")
	}
	for _, key := range keys {
		if len(key) < minAPIKeyLength {
			return fmt.Errorf("each API key must be at least %d characters", minAPIKeyLength)
		}
	}
	return nil
}

func isAPIVersion(version string) bool {
	numbers, found := strings.CutPrefix(version, "v")
	if !found {
		return false
	}
	major, minor, found := strings.Cut(numbers, ".")
	if !found {
		return false
	}
	for _, part := range []string{major, minor} {
		if _, err := strconv.ParseUint(part, 10, 16); err != nil {
			return false
		}
	}
	return true
}
