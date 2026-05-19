// Package tools — реализация MCP-tools и их связка с MCP-сервером.
// Зависит от metabase/schema/cache/sqlguard. Сам не знает про HTTP.
package tools

import (
	"context"
	"log/slog"

	"github.com/dezer32/metabase-mcp/internal/cache"
	"github.com/dezer32/metabase-mcp/internal/metabase"
)

// MetabaseClient — узкий интерфейс над *metabase.Client.
// Введён ради подмены в тестах: можно встроить mock без поднятия httptest.
type MetabaseClient interface {
	Databases(ctx context.Context) ([]metabase.Database, error)
	Metadata(ctx context.Context, databaseID int) (*metabase.MetadataRaw, error)
	Dataset(ctx context.Context, databaseID int, query string, rowLimit int) (*metabase.DatasetResponse, error)
}

// Deps — пакетные зависимости tools. Передаются в Register.
type Deps struct {
	MB        MetabaseClient
	Databases *cache.Cache[string, []metabase.Database] // ключ — databasesCacheKey
	Metadata  *cache.Cache[int, *metabase.MetadataRaw]  // ключ — database_id
	Log       *slog.Logger
}
