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
	"github.com/dezer32/metabase-mcp/internal/spool"
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

// Instance — собранный сервер вместе с ресурсами, которые нужно освободить
// после Run. Сам MCP-сервер лежит в MCP и передаётся в Run.
type Instance struct {
	MCP   *mcp.Server
	spool *spool.Store
}

// Close освобождает ресурсы инстанса: удаляет спуленные файлы результатов,
// созданные этим процессом. Каталог остаётся — его может делить другой инстанс.
func (i *Instance) Close() error {
	if i == nil || i.spool == nil {
		return nil
	}
	return i.spool.Close()
}

// New собирает сервер: создаёт metabase-клиент (по режиму аутентификации),
// кэши, спул результатов и регистрирует tool'ы. Сам сервер не стартует —
// на это есть transport.go.
//
// В oauth-режиме здесь же может произойти интерактивный вход (браузер + stderr),
// поэтому New принимает ctx — Ctrl-C во время входа прерывает его.
//
// transport нужен не для запуска, а чтобы решить, показывать ли клиенту
// локальный путь к файлу результата: на stdio клиент — процесс на той же
// машине, на будущем HTTP путь был бы вредной дезинформацией.
func New(ctx context.Context, cfg config.Config, transport string, log *slog.Logger) (*Instance, error) {
	mb, err := buildClient(ctx, cfg, log)
	if err != nil {
		return nil, err
	}

	deps := tools.Deps{
		MB:        mb,
		Databases: cache.New[string, []metabase.Database](cacheTTL),
		Metadata:  cache.New[int, *metabase.MetadataRaw](cacheTTL),
		Log:       log,
		Limits: tools.Limits{
			InlineMaxBytes:  cfg.ResultInlineMaxBytes,
			PreviewRows:     cfg.ResultPreviewRows,
			AutoLimit:       cfg.ExecuteSQLAutoLimit,
			ExposeLocalPath: isLocalTransport(transport),
		},
	}

	st := buildSpool(cfg, log)
	// Присваиваем ТОЛЬКО непустой store: типизированный nil в интерфейсе
	// дал бы d.Spool != nil и панику на первом же большом результате.
	if st != nil {
		deps.Spool = st
	}

	srv := mcp.NewServer(Implementation, nil)
	// Register обязан быть до Run: capability resources сервер вычисляет
	// один раз на initialize по наличию шаблона ресурса.
	tools.Register(srv, deps)
	return &Instance{MCP: srv, spool: st}, nil
}

// buildSpool создаёт спул результатов. Ошибка создания каталога НЕ фатальна:
// в distroless-образе под nonroot /tmp может быть недоступен на запись, и
// падение на старте сломало бы всех docker-пользователей. Логируем Warn и
// работаем inline-only.
func buildSpool(cfg config.Config, log *slog.Logger) *spool.Store {
	if !cfg.ResultSpoolEnabled {
		log.Info("result spool disabled: execute_sql always returns rows inline")
		return nil
	}
	st, err := spool.New(spool.Config{
		Dir:      cfg.ResultSpoolDir,
		TTL:      cfg.ResultSpoolTTL,
		MaxBytes: cfg.ResultSpoolMaxBytes,
	}, log)
	if err != nil {
		log.Warn("result spool unavailable, large results will be returned inline",
			slog.String("err", err.Error()))
		return nil
	}
	log.Info("result spool ready",
		slog.String("dir", st.Dir()),
		slog.Duration("ttl", cfg.ResultSpoolTTL),
		slog.Int64("max_bytes", cfg.ResultSpoolMaxBytes),
		slog.Int("inline_max_bytes", cfg.ResultInlineMaxBytes),
	)
	return st
}

// isLocalTransport — клиент работает на той же машине, что и сервер?
// Только тогда локальный путь к файлу результата ему полезен.
func isLocalTransport(transport string) bool {
	return transport == "" || transport == "stdio"
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
