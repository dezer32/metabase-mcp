// Package config читает конфигурацию из переменных окружения.
// Используется один-единственный раз на старте main(). Дальше передаётся
// по значению. Никаких глобалов.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config — заполненная конфигурация сервера.
type Config struct {
	MetabaseURL      string        // без trailing slash
	MetabaseUser     string        // login
	MetabasePassword string        // password
	LogLevel         string        // debug|info|warn|error (lowercase)
	HTTPTimeout      time.Duration // таймаут HTTP-клиента к Metabase
}

// Load читает все нужные env-переменные и валидирует их.
// Возвращает первую же ошибку с понятным префиксом.
func Load() (Config, error) {
	cfg := Config{
		MetabaseURL:      strings.TrimRight(os.Getenv("METABASE_URL"), "/"),
		MetabaseUser:     os.Getenv("METABASE_USER"),
		MetabasePassword: os.Getenv("METABASE_PASSWORD"),
		LogLevel:         strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))),
		HTTPTimeout:      30 * time.Second,
	}

	if cfg.MetabaseURL == "" {
		return Config{}, errors.New("METABASE_URL is required")
	}
	if _, err := url.ParseRequestURI(cfg.MetabaseURL); err != nil {
		return Config{}, fmt.Errorf("METABASE_URL invalid: %w", err)
	}
	if cfg.MetabaseUser == "" {
		return Config{}, errors.New("METABASE_USER is required")
	}
	if cfg.MetabasePassword == "" {
		return Config{}, errors.New("METABASE_PASSWORD is required")
	}

	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("LOG_LEVEL invalid: %q (allowed: debug|info|warn|error)", cfg.LogLevel)
	}

	if raw := os.Getenv("HTTP_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HTTP_TIMEOUT invalid: %w", err)
		}
		cfg.HTTPTimeout = d
	}

	return cfg, nil
}
