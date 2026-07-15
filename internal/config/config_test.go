package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// allEnv — полный список env-переменных, которые читает config.
// withEnv чистит их все, чтобы тесты не текли друг в друга.
// HOME намеренно НЕ чистим: он нужен os.UserHomeDir() для дефолта token-file.
var allEnv = []string{
	"METABASE_URL",
	"METABASE_USER",
	"METABASE_PASSWORD",
	"LOG_LEVEL",
	"HTTP_TIMEOUT",
	"METABASE_OAUTH_RESOURCE",
	"METABASE_OAUTH_SCOPES",
	"METABASE_TOKEN_FILE",
	"METABASE_OAUTH_REDIRECT_ADDR",
	"METABASE_OAUTH_LOGIN_TIMEOUT",
	"METABASE_OAUTH_CLIENT_ID",
	"METABASE_OAUTH_CLIENT_SECRET",
	"METABASE_OAUTH_NONINTERACTIVE",
	"METABASE_OAUTH_RESOURCE_ON_REFRESH",
	"XDG_CONFIG_HOME",
}

// withEnv устанавливает env-переменные на время теста, остальные чистит.
func withEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for _, k := range allEnv {
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
	// Заданы user+password → password-режим.
	if cfg.AuthMode != AuthModePassword {
		t.Errorf("AuthMode: got %q, want %q", cfg.AuthMode, AuthModePassword)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default: got %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.HTTPTimeout != 30*time.Second {
		t.Errorf("HTTPTimeout default: got %v", cfg.HTTPTimeout)
	}
}

func TestLoad_MissingURL(t *testing.T) {
	withEnv(t, map[string]string{"METABASE_USER": "u", "METABASE_PASSWORD": "p"})
	_, err := Load()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "METABASE_URL") {
		t.Errorf("error %q should mention METABASE_URL", err.Error())
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

// TestLoad_OAuthMode — ни user, ни password → oauth-режим со всеми дефолтами.
func TestLoad_OAuthMode(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL": "https://mb.example.com",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AuthMode != AuthModeOAuth {
		t.Fatalf("AuthMode: got %q, want %q", cfg.AuthMode, AuthModeOAuth)
	}
	if cfg.OAuthResource != "https://mb.example.com/api/metabase-mcp" {
		t.Errorf("OAuthResource default: got %q", cfg.OAuthResource)
	}
	if len(cfg.OAuthScopes) != 1 || cfg.OAuthScopes[0] != "mb:full" {
		t.Errorf("OAuthScopes default: got %v", cfg.OAuthScopes)
	}
	if cfg.OAuthRedirectAddr != "127.0.0.1:0" {
		t.Errorf("OAuthRedirectAddr default: got %q", cfg.OAuthRedirectAddr)
	}
	if cfg.OAuthLoginTimeout != 3*time.Minute {
		t.Errorf("OAuthLoginTimeout default: got %v", cfg.OAuthLoginTimeout)
	}
	if cfg.OAuthNoninteractive {
		t.Errorf("OAuthNoninteractive default should be false")
	}
}

// TestLoad_ModeMismatch — ровно один из user/password → ошибка конфигурации.
func TestLoad_ModeMismatch(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		errFrag string
	}{
		{
			name:    "user without password",
			env:     map[string]string{"METABASE_URL": "https://x", "METABASE_USER": "u"},
			errFrag: "METABASE_PASSWORD",
		},
		{
			name:    "password without user",
			env:     map[string]string{"METABASE_URL": "https://x", "METABASE_PASSWORD": "p"},
			errFrag: "METABASE_USER",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, tc.env)
			_, err := Load()
			if err == nil {
				t.Fatalf("expected mode-mismatch error, got nil")
			}
			if !strings.Contains(err.Error(), tc.errFrag) {
				t.Errorf("error %q should mention %q", err.Error(), tc.errFrag)
			}
		})
	}
}

func TestLoad_OAuthResourceOverride(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":            "https://mb.example.com",
		"METABASE_OAUTH_RESOURCE": "https://mb.example.com/api/custom-mcp",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OAuthResource != "https://mb.example.com/api/custom-mcp" {
		t.Errorf("OAuthResource override: got %q", cfg.OAuthResource)
	}
}

func TestLoad_OAuthScopesParsing(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"space-separated", "mb:full agent:read", []string{"mb:full", "agent:read"}},
		{"comma-separated", "mb:full, agent:read", []string{"mb:full", "agent:read"}},
		{"mixed with extra spaces", "  mb:full ,  agent:read  ", []string{"mb:full", "agent:read"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, map[string]string{
				"METABASE_URL":          "https://x",
				"METABASE_OAUTH_SCOPES": tc.raw,
			})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.OAuthScopes) != len(tc.want) {
				t.Fatalf("OAuthScopes: got %v, want %v", cfg.OAuthScopes, tc.want)
			}
			for i := range tc.want {
				if cfg.OAuthScopes[i] != tc.want[i] {
					t.Errorf("OAuthScopes[%d]: got %q, want %q", i, cfg.OAuthScopes[i], tc.want[i])
				}
			}
		})
	}
}

func TestLoad_OAuthScopesEmptyIsError(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":          "https://x",
		"METABASE_OAUTH_SCOPES": "   ,  ,  ",
	})
	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for empty scopes list")
	}
	if !strings.Contains(err.Error(), "METABASE_OAUTH_SCOPES") {
		t.Errorf("error should mention METABASE_OAUTH_SCOPES: %v", err)
	}
}

func TestLoad_TokenFileDefault_XDG(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":    "https://x",
		"XDG_CONFIG_HOME": "/tmp/xdgcfg",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/tmp/xdgcfg", "metabase-mcp", "token.json")
	if cfg.TokenFile != want {
		t.Errorf("TokenFile (XDG): got %q, want %q", cfg.TokenFile, want)
	}
}

func TestLoad_TokenFileDefault_Home(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL": "https://x",
		"HOME":         "/tmp/homecfg",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/tmp/homecfg", ".config", "metabase-mcp", "token.json")
	if cfg.TokenFile != want {
		t.Errorf("TokenFile (HOME): got %q, want %q", cfg.TokenFile, want)
	}
}

func TestLoad_TokenFileOverride(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":        "https://x",
		"METABASE_TOKEN_FILE": "/custom/place/tok.json",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TokenFile != "/custom/place/tok.json" {
		t.Errorf("TokenFile override: got %q", cfg.TokenFile)
	}
}

func TestLoad_RedirectAddrValidation(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"loopback ipv4", "127.0.0.1:0", false},
		{"loopback ipv4 with port", "127.0.0.1:54321", false},
		{"localhost", "localhost:8080", false},
		{"loopback ipv6", "[::1]:0", false},
		{"wildcard is not loopback", "0.0.0.0:8080", true},
		{"public ip", "1.2.3.4:80", true},
		{"missing port", "127.0.0.1", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withEnv(t, map[string]string{
				"METABASE_URL":                 "https://x",
				"METABASE_OAUTH_REDIRECT_ADDR": tc.addr,
			})
			_, err := Load()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for addr %q", tc.addr)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for addr %q: %v", tc.addr, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "METABASE_OAUTH_REDIRECT_ADDR") {
				t.Errorf("error should mention METABASE_OAUTH_REDIRECT_ADDR: %v", err)
			}
		})
	}
}

func TestLoad_ClientSecretWithoutID(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":                 "https://x",
		"METABASE_OAUTH_CLIENT_SECRET": "shhh",
	})
	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for secret without client id")
	}
	if !strings.Contains(err.Error(), "METABASE_OAUTH_CLIENT_ID") {
		t.Errorf("error should mention METABASE_OAUTH_CLIENT_ID: %v", err)
	}
}

func TestLoad_ClientIDWithoutSecretOK(t *testing.T) {
	// Public client: CLIENT_ID без секрета — допустимо.
	withEnv(t, map[string]string{
		"METABASE_URL":             "https://x",
		"METABASE_OAUTH_CLIENT_ID": "public-client",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OAuthClientID != "public-client" {
		t.Errorf("OAuthClientID: got %q", cfg.OAuthClientID)
	}
	if cfg.OAuthClientSecret != "" {
		t.Errorf("OAuthClientSecret should be empty")
	}
}

func TestLoad_LoginTimeoutOverride(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":                 "https://x",
		"METABASE_OAUTH_LOGIN_TIMEOUT": "90s",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OAuthLoginTimeout != 90*time.Second {
		t.Errorf("OAuthLoginTimeout: got %v", cfg.OAuthLoginTimeout)
	}
}

func TestLoad_NoninteractiveFlag(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":                  "https://x",
		"METABASE_OAUTH_NONINTERACTIVE": "true",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.OAuthNoninteractive {
		t.Errorf("OAuthNoninteractive should be true")
	}
}

func TestLoad_ResourceOnRefreshDefault(t *testing.T) {
	withEnv(t, map[string]string{"METABASE_URL": "https://x"})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// По умолчанию — слать resource на refresh (безопасно для RFC 8707 AS).
	if !cfg.OAuthResourceOnRefresh {
		t.Errorf("OAuthResourceOnRefresh default should be true")
	}
}

func TestLoad_ResourceOnRefreshOverride(t *testing.T) {
	withEnv(t, map[string]string{
		"METABASE_URL":                       "https://x",
		"METABASE_OAUTH_RESOURCE_ON_REFRESH": "false",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OAuthResourceOnRefresh {
		t.Errorf("OAuthResourceOnRefresh should be false when overridden")
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
