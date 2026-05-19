// Package server — фабрика MCP-сервера со всеми зарегистрированными tool'ами.
package server

import (
	"log/slog"
	"time"

	"github.com/dezer32/metabase-mcp/internal/cache"
	"github.com/dezer32/metabase-mcp/internal/config"
	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/dezer32/metabase-mcp/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Implementation — версионная метка нашего сервера, видимая клиенту.
var Implementation = &mcp.Implementation{
	Name:    "metabase-mcp",
	Version: "0.1.0",
}

// cacheTTL применяется ко всем кэшам tools-слоя.
const cacheTTL = 5 * time.Minute

// New собирает сервер: создаёт metabase-клиент, кэши и регистрирует tool'ы.
// Сам сервер не стартует — на это есть transport.go.
func New(cfg config.Config, log *slog.Logger) *mcp.Server {
	mb := metabase.NewClient(cfg.MetabaseURL, cfg.MetabaseUser, cfg.MetabasePassword,
		cfg.HTTPTimeout, log)

	deps := tools.Deps{
		MB:        mb,
		Databases: cache.New[string, []metabase.Database](cacheTTL),
		Metadata:  cache.New[int, *metabase.MetadataRaw](cacheTTL),
		Log:       log,
	}

	srv := mcp.NewServer(Implementation, nil)
	tools.Register(srv, deps)
	return srv
}
