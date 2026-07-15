// Package config читает конфигурацию из переменных окружения.
// Используется один-единственный раз на старте main(). Дальше передаётся
// по значению. Никаких глобалов.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Режимы аутентификации к Metabase.
const (
	// AuthModePassword — классический POST /api/session (логин+пароль).
	AuthModePassword = "password"
	// AuthModeOAuth — OAuth 2.1 Authorization Code + PKCE + refresh.
	AuthModeOAuth = "oauth"
)

// Config — заполненная конфигурация сервера.
type Config struct {
	MetabaseURL      string        // без trailing slash
	MetabaseUser     string        // login (опц., только password-режим)
	MetabasePassword string        // password (опц., только password-режим)
	LogLevel         string        // debug|info|warn|error (lowercase)
	HTTPTimeout      time.Duration // таймаут HTTP-клиента к Metabase

	// AuthMode вычисляется по наличию user/password:
	// оба заданы → password, ни одного → oauth, ровно один → ошибка.
	AuthMode string

	// OAuth-поля (используются только в AuthModeOAuth, но парсятся всегда).
	OAuthResource          string        // RFC 8707 resource; дефолт URL+/api/metabase-mcp
	OAuthScopes            []string      // запрашиваемые scope; дефолт [mb:full]
	OAuthClientID          string        // опц.; задан → DCR пропускается
	OAuthClientSecret      string        // опц.; задан → confidential client
	OAuthRedirectAddr      string        // loopback addr для колбэка; дефолт 127.0.0.1:0
	OAuthLoginTimeout      time.Duration // таймаут интерактивного входа; дефолт 3m
	OAuthNoninteractive    bool          // true → не поднимать браузер/DCR, только refresh
	OAuthResourceOnRefresh bool          // слать RFC 8707 resource на refresh; дефолт true
	TokenFile              string        // путь к персисту токена
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

	// Режим по наличию user/password.
	hasUser := cfg.MetabaseUser != ""
	hasPass := cfg.MetabasePassword != ""
	switch {
	case hasUser && hasPass:
		cfg.AuthMode = AuthModePassword
	case !hasUser && !hasPass:
		cfg.AuthMode = AuthModeOAuth
	case hasUser && !hasPass:
		return Config{}, errors.New("METABASE_USER is set but METABASE_PASSWORD is empty: " +
			"set both (password mode) or neither (oauth mode)")
	default: // hasPass && !hasUser
		return Config{}, errors.New("METABASE_PASSWORD is set but METABASE_USER is empty: " +
			"set both (password mode) or neither (oauth mode)")
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

	if err := loadOAuth(&cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// loadOAuth парсит OAuth-поля и проставляет дефолты. Валидирует формат
// (loopback-адрес, длительность, scopes, secret-без-id) независимо от режима:
// заданная в env некорректность — ошибка в любом режиме.
func loadOAuth(cfg *Config) error {
	// resource: дефолт METABASE_URL + /api/metabase-mcp.
	cfg.OAuthResource = strings.TrimSpace(os.Getenv("METABASE_OAUTH_RESOURCE"))
	if cfg.OAuthResource == "" {
		cfg.OAuthResource = cfg.MetabaseURL + "/api/metabase-mcp"
	}

	// scopes: space/comma-separated; дефолт mb:full.
	if raw := os.Getenv("METABASE_OAUTH_SCOPES"); strings.TrimSpace(raw) == "" {
		cfg.OAuthScopes = []string{"mb:full"}
	} else {
		scopes := parseScopes(raw)
		if len(scopes) == 0 {
			return errors.New("METABASE_OAUTH_SCOPES is set but contains no scopes")
		}
		cfg.OAuthScopes = scopes
	}

	// client id/secret. Секрет без id — ошибка (blocker-3).
	cfg.OAuthClientID = strings.TrimSpace(os.Getenv("METABASE_OAUTH_CLIENT_ID"))
	cfg.OAuthClientSecret = os.Getenv("METABASE_OAUTH_CLIENT_SECRET")
	if cfg.OAuthClientSecret != "" && cfg.OAuthClientID == "" {
		return errors.New("METABASE_OAUTH_CLIENT_SECRET is set but METABASE_OAUTH_CLIENT_ID is empty")
	}

	// redirect addr: дефолт 127.0.0.1:0; валидируем loopback-only.
	cfg.OAuthRedirectAddr = strings.TrimSpace(os.Getenv("METABASE_OAUTH_REDIRECT_ADDR"))
	if cfg.OAuthRedirectAddr == "" {
		cfg.OAuthRedirectAddr = "127.0.0.1:0"
	}
	if err := validateLoopbackAddr(cfg.OAuthRedirectAddr); err != nil {
		return fmt.Errorf("METABASE_OAUTH_REDIRECT_ADDR invalid: %w", err)
	}

	// login timeout: дефолт 3m.
	cfg.OAuthLoginTimeout = 3 * time.Minute
	if raw := os.Getenv("METABASE_OAUTH_LOGIN_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("METABASE_OAUTH_LOGIN_TIMEOUT invalid: %w", err)
		}
		cfg.OAuthLoginTimeout = d
	}

	// noninteractive flag (дефолт false).
	ni, err := parseBool("METABASE_OAUTH_NONINTERACTIVE", false)
	if err != nil {
		return err
	}
	cfg.OAuthNoninteractive = ni

	// resource на refresh (дефолт true — безопасно для RFC 8707 AS).
	ror, err := parseBool("METABASE_OAUTH_RESOURCE_ON_REFRESH", true)
	if err != nil {
		return err
	}
	cfg.OAuthResourceOnRefresh = ror

	// token file: явный путь или дефолт XDG_CONFIG_HOME / $HOME/.config.
	// В password-режиме файл токена не нужен, поэтому невозможность вычислить
	// путь (например, нет $HOME) — не ошибка. В oauth-режиме — фатально.
	tf, err := resolveTokenFile()
	if err != nil {
		if cfg.AuthMode == AuthModeOAuth {
			return err
		}
	} else {
		cfg.TokenFile = tf
	}

	return nil
}

// parseBool читает булев env по имени. Пусто → def. Иначе принимает
// 1/true/yes/on и 0/false/no/off; на прочее — ошибка с именем переменной.
func parseBool(envName string, def bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return def, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s invalid: %q (allowed: true|false)", envName, raw)
	}
}

// parseScopes режет строку на scope'ы по пробелам и запятым, отбрасывая пустые.
func parseScopes(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// validateLoopbackAddr проверяет, что host:port указывает на loopback.
// Разрешён "localhost" и любой loopback-IP (127.0.0.0/8, ::1).
func validateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("expected host:port: %w", err)
	}
	if port == "" {
		return errors.New("port is required")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("host %q is not an IP or localhost", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("host %q is not a loopback address", host)
	}
	return nil
}

// resolveTokenFile возвращает путь к файлу токена.
// Приоритет: METABASE_TOKEN_FILE → XDG_CONFIG_HOME → $HOME/.config.
// Намеренно НЕ используем os.UserConfigDir(): на macOS он даёт ~/Library/...
func resolveTokenFile() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("METABASE_TOKEN_FILE")); explicit != "" {
		return explicit, nil
	}
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine token file location: set METABASE_TOKEN_FILE (%w)", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "metabase-mcp", "token.json"), nil
}
