package config

import (
	"strings"
	"testing"
	"time"
)

// withEnv устанавливает env-переменные на время теста, остальные чистит.
// Используется во всех тестах конфигурации, чтобы не было утечек состояния.
func withEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	known := []string{
		"METABASE_URL",
		"METABASE_USER",
		"METABASE_PASSWORD",
		"LOG_LEVEL",
		"HTTP_TIMEOUT",
	}
	for _, k := range known {
		t.Setenv(k, "")
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func TestLoad_OK(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":      "https://metabase.example.com/",
		"METABASE_USER":     "user@example.com",
		"METABASE_PASSWORD": "s3cret",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Trailing slash должен быть очищен.
	if cfg.MetabaseURL != "https://metabase.example.com" {
		t.Errorf("MetabaseURL: got %q", cfg.MetabaseURL)
	}
	if cfg.MetabaseUser != "user@example.com" {
		t.Errorf("MetabaseUser: got %q", cfg.MetabaseUser)
	}
	if cfg.MetabasePassword != "s3cret" {
		t.Errorf("MetabasePassword: got %q", cfg.MetabasePassword)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default: got %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.HTTPTimeout != 30*time.Second {
		t.Errorf("HTTPTimeout default: got %v", cfg.HTTPTimeout)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		errFrag string
	}{
		{
			name:    "no URL",
			env:     map[string]string{"METABASE_USER": "u", "METABASE_PASSWORD": "p"},
			errFrag: "METABASE_URL",
		},
		{
			name:    "no USER",
			env:     map[string]string{"METABASE_URL": "https://x", "METABASE_PASSWORD": "p"},
			errFrag: "METABASE_USER",
		},
		{
			name:    "no PASSWORD",
			env:     map[string]string{"METABASE_URL": "https://x", "METABASE_USER": "u"},
			errFrag: "METABASE_PASSWORD",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, tc.env)
			_, err := Load()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.errFrag) {
				t.Errorf("error %q should mention %q", err.Error(), tc.errFrag)
			}
		})
	}
}

func TestLoad_InvalidURL(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":      "not a url at all",
		"METABASE_USER":     "u",
		"METABASE_PASSWORD": "p",
	})
	_, err := Load()
	if err == nil {
		t.Fatalf("expected URL parse error")
	}
	if !strings.Contains(err.Error(), "METABASE_URL") {
		t.Errorf("error should mention METABASE_URL: %v", err)
	}
}

func TestLoad_LogLevelOverride(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":      "https://x",
		"METABASE_USER":     "u",
		"METABASE_PASSWORD": "p",
		"LOG_LEVEL":         "DEBUG",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel should be lowercased: got %q", cfg.LogLevel)
	}
}

func TestLoad_LogLevelInvalid(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":      "https://x",
		"METABASE_USER":     "u",
		"METABASE_PASSWORD": "p",
		"LOG_LEVEL":         "verbose",
	})
	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for invalid LOG_LEVEL")
	}
}

func TestLoad_HTTPTimeoutOverride(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":      "https://x",
		"METABASE_USER":     "u",
		"METABASE_PASSWORD": "p",
		"HTTP_TIMEOUT":      "5s",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPTimeout != 5*time.Second {
		t.Errorf("HTTPTimeout: got %v", cfg.HTTPTimeout)
	}
}

func TestLoad_HTTPTimeoutInvalid(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":      "https://x",
		"METABASE_USER":     "u",
		"METABASE_PASSWORD": "p",
		"HTTP_TIMEOUT":      "not-a-duration",
	})
	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for invalid HTTP_TIMEOUT")
	}
}
