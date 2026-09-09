// Package tools — реализация MCP-tools и их связка с MCP-сервером.
// Зависит от metabase/schema/cache/sqlguard. Сам не знает про HTTP.
package tools

import (
	"context"
	"log/slog"

	"github.com/dezer32/metabase-mcp/internal/cache"
	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/dezer32/metabase-mcp/internal/spool"
)

// MetabaseClient — узкий интерфейс над *metabase.Client.
// Введён ради подмены в тестах: можно встроить mock без поднятия httptest.
type MetabaseClient interface {
	Databases(ctx context.Context) ([]metabase.Database, error)
	Metadata(ctx context.Context, databaseID int) (*metabase.MetadataRaw, error)
	Dataset(ctx context.Context, databaseID int, query string, rowLimit int) (*metabase.DatasetResponse, error)
}

// Spool — узкий интерфейс спула больших результатов, как и MetabaseClient,
// ради подмены в тестах. nil означает «спул выключен»: тогда результат
// всегда уходит инлайном, а шаблон ресурса не регистрируется.
type Spool interface {
	Put(ndjson []byte, rowCount int, meta map[string]any) (spool.Entry, error)
	ReadAll(id string) ([]byte, spool.Entry, error)
}

// Deps — пакетные зависимости tools. Передаются в Register.
type Deps struct {
	MB        MetabaseClient
	Databases *cache.Cache[string, []metabase.Database] // ключ — databasesCacheKey
	Metadata  *cache.Cache[int, *metabase.MetadataRaw]  // ключ — database_id
	Log       *slog.Logger
	Limits    Limits
	Spool     Spool // nil → спул выключен
}

// Limits — пороги отдачи результата execute_sql. Нулевое значение означает
// «фича выключена»: всегда inline, LIMIT не дописываем, превью нет — так
// тесты, собирающие Deps руками, продолжают работать без правок.
// Боевые значения — в DefaultLimits.
type Limits struct {
	InlineMaxBytes  int  // 0 → всегда inline
	PreviewRows     int  // 0 → без превью
	AutoLimit       bool // false → LIMIT не дописываем
	ExposeLocalPath bool // false → ResultRef.Path пустой
}

// DefaultLimits — боевые дефолты, те же, что у env-переменных
// RESULT_INLINE_MAX_BYTES / RESULT_PREVIEW_ROWS / EXECUTE_SQL_AUTO_LIMIT.
//
// ExposeLocalPath намеренно false: это не настройка, а производная
// транспорта (stdio — клиент на той же машине, путь полезен), её
// проставляет server.New.
func DefaultLimits() Limits {
	return Limits{
		InlineMaxBytes: 64 << 10, // 64 KiB
		PreviewRows:    5,
		AutoLimit:      true,
	}
}
