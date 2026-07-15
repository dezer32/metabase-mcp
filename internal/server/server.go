// Package server — фабрика MCP-сервера со всеми зарегистрированными tool'ами.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/dezer32/metabase-mcp/internal/cache"
	"github.com/dezer32/metabase-mcp/internal/config"
	"github.com/dezer32/metabase-mcp/internal/metabase"
	mboauth "github.com/dezer32/metabase-mcp/internal/metabase/oauth"
	"github.com/dezer32/metabase-mcp/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// Implementation — версионная метка нашего сервера, видимая клиенту.
var Implementation = &mcp.Implementation{
	Name:    "metabase-mcp",
	Version: "0.1.0",
}

// cacheTTL применяется ко всем кэшам tools-слоя.
const cacheTTL = 5 * time.Minute

// New собирает сервер: создаёт metabase-клиент (по режиму аутентификации),
// кэши и регистрирует tool'ы. Сам сервер не стартует — на это есть transport.go.
//
// В oauth-режиме здесь же может произойти интерактивный вход (браузер + stderr),
// поэтому New принимает ctx — Ctrl-C во время входа прерывает его.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*mcp.Server, error) {
	mb, err := buildClient(ctx, cfg, log)
	if err != nil {
		return nil, err
	}

	deps := tools.Deps{
		MB:        mb,
		Databases: cache.New[string, []metabase.Database](cacheTTL),
		Metadata:  cache.New[int, *metabase.MetadataRaw](cacheTTL),
		Log:       log,
	}

	srv := mcp.NewServer(Implementation, nil)
	tools.Register(srv, deps)
	return srv, nil
}

// buildClient строит metabase-клиент под выбранный режим аутентификации.
func buildClient(ctx context.Context, cfg config.Config, log *slog.Logger) (*metabase.Client, error) {
	switch cfg.AuthMode {
	case config.AuthModePassword:
		return metabase.NewClient(cfg.MetabaseURL, cfg.MetabaseUser, cfg.MetabasePassword,
			cfg.HTTPTimeout, log), nil

	case config.AuthModeOAuth:
		// Единый bounded HTTP-клиент: он же кладётся в ctx для token-операций
		// (Exchange/refresh через oauth2.HTTPClient), он же используется для
		// discovery, DCR и data-запросов.
		hc := &http.Client{Timeout: cfg.HTTPTimeout}
		tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, hc)
		mgr, err := mboauth.NewTokenSource(tokenCtx, cfg, hc, log)
		if err != nil {
			return nil, fmt.Errorf("oauth setup: %w", err)
		}
		return metabase.NewOAuthClient(cfg.MetabaseURL, mgr, hc, log), nil

	default:
		return nil, fmt.Errorf("unknown auth mode %q", cfg.AuthMode)
	}
}
