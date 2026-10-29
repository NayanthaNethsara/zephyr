package config

import (
	"fmt"
	"strings"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Port     int    `koanf:"gateway_port"`
	LogLevel string `koanf:"log_level"`
}

var defaults = map[string]any{
	"gateway_port": 8080,
	"log_level":    "info",
}

func Load() (Config, error) {
	k := koanf.New(".")

	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
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

func (c Config) validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("GATEWAY_PORT must be between 1 and 65535, got %d", c.Port)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
		return nil
	}
	return fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error, got %q", c.LogLevel)
}
