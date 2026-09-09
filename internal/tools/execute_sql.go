package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/dezer32/metabase-mcp/internal/schema"
	"github.com/dezer32/metabase-mcp/internal/spool"
	"github.com/dezer32/metabase-mcp/internal/sqlguard"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type executeSQLIn struct {
	DatabaseID int    `json:"database_id" jsonschema:"id of a database returned by list_databases"`
	Query      string `json:"query" jsonschema:"SQL to execute; only SELECT and WITH allowed. ALWAYS include an explicit LIMIT, and OFFSET when paging: pagination is done in the SQL itself. If LIMIT is missing the server appends one and reports it in meta.warnings."`
	RowLimit   int    `json:"row_limit,omitempty" jsonschema:"safety cap handed to Metabase constraints, and the value used if the server has to append a LIMIT (default 1000, max 50000). It is NOT pagination - put LIMIT/OFFSET in the query."`
	Delivery   string `json:"delivery,omitempty" jsonschema:"how to return rows: auto (default, file only when the result is large), inline (always rows), file (always a resource link to a local NDJSON file)"`
}

type executeSQLOut = schema.Result

const (
	defaultRowLimit = 1000
	maxRowLimit     = 50000
)

// Значения meta.delivery — они же допустимые значения аргумента delivery
// (плюс deliveryAuto).
const (
	deliveryAuto   = "auto"
	deliveryInline = "inline"
	deliveryFile   = "file"
)

const executeSQLDesc = "Executes a read-only SQL query against a database via Metabase. " +
	"Only SELECT and WITH (CTE) are allowed. " +
	"Use the engine from list_databases to pick the right SQL dialect. " +
	"Pagination lives in the SQL itself: ALWAYS pass an explicit LIMIT, and OFFSET to " +
	"walk pages. If the query has no top-level LIMIT the server appends " +
	`"LIMIT <row_limit> OFFSET 0", puts the executed SQL in meta.effective_sql ` +
	"and a note in meta.warnings; meta.next_offset points at the next page. " +
	"Returns rows as objects with column names. " +
	"A large result is written to a local NDJSON file instead of being inlined: " +
	"rows is then null, meta.delivery is \"file\" and resource holds the link plus " +
	"a short preview. Force it either way with delivery=inline|file. " +
	"Parameters: database_id (int, from list_databases), query (string), " +
	"row_limit (int, optional, default 1000, max 50000), " +
	"delivery (string, optional, auto|inline|file)."

func registerExecuteSQL(server *mcp.Server, d Deps) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "execute_sql",
			Description: executeSQLDesc,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in executeSQLIn) (*mcp.CallToolResult, executeSQLOut, error) {
			if in.DatabaseID <= 0 {
				return nil, executeSQLOut{}, fmt.Errorf("execute_sql: database_id must be positive (got %d)", in.DatabaseID)
			}
			delivery, err := normalizeDelivery(in.Delivery)
			if err != nil {
				return nil, executeSQLOut{}, fmt.Errorf("execute_sql: %w", err)
			}
			// Валидация ДО Metabase: блокируем DROP, INSERT, multi-statement и
			// т.п. Тот же парс отдаёт факты о верхнеуровневом LIMIT/OFFSET.
			info, err := sqlguard.Inspect(in.Query)
			if err != nil {
				return nil, executeSQLOut{}, fmt.Errorf("execute_sql: %w", err)
			}
			limit := normalizeLimit(in.RowLimit)
			effective, page, warns := maybeAppendLimit(in.Query, info, limit, d.Limits.AutoLimit)

			resp, err := d.MB.Dataset(ctx, in.DatabaseID, effective, limit)
			if err != nil {
				return nil, executeSQLOut{}, fmt.Errorf("execute_sql: %w", err)
			}

			res := schema.Rows(resp.Data.Cols, resp.Data.Rows, resp.RunningTime)
			res.Meta.Delivery = deliveryInline
			res.Meta.NextOffset = nextOffset(page)
			res.Meta.Warnings = warns
			if effective != in.Query {
				res.Meta.EffectiveSQL = effective
			}
			if n, ok := resp.Data.Truncated(); ok {
				res.Meta.Truncated = true
				res.Meta.TruncatedAt = n
				res.Meta.Warnings = append(res.Meta.Warnings, truncatedWarning(n))
			}

			// NDJSON считаем один раз: в file-режиме он и так нужен, а
			// json.Marshal всего результата ради одной проверки размера —
			// лишняя работа.
			ndjson, err := spool.Encode(res.Rows)
			if err != nil {
				return nil, executeSQLOut{}, fmt.Errorf("execute_sql: %w", err)
			}
			if wantFile(delivery, len(ndjson), d.Limits) {
				if d.Spool == nil {
					if delivery == deliveryFile {
						res.Meta.Warnings = append(res.Meta.Warnings,
							"delivery=file was requested but the result spool is disabled: returning rows inline")
					}
					return nil, res, nil
				}
				e, perr := d.Spool.Put(ndjson, len(res.Rows), resourceMeta(res))
				if perr != nil {
					// Спул сломался — запрос НЕ роняем, отдаём инлайном.
					d.Log.Warn("execute_sql: spool failed, falling back to inline",
						slog.String("err", perr.Error()))
					res.Meta.Warnings = append(res.Meta.Warnings,
						"result is large but could not be spooled: returning it inline")
					return nil, res, nil
				}
				return fileResult(res, e, d.Limits)
			}
			return nil, res, nil
		},
	)
}

// normalizeDelivery приводит аргумент delivery к каноническому виду.
// Пусто → auto.
func normalizeDelivery(v string) (string, error) {
	switch d := strings.ToLower(strings.TrimSpace(v)); d {
	case "", deliveryAuto:
		return deliveryAuto, nil
	case deliveryInline, deliveryFile:
		return d, nil
	default:
		return "", fmt.Errorf("delivery must be one of auto|inline|file (got %q)", v)
	}
}

// wantFile решает, уходит ли результат в файл. delivery=file → всегда,
// inline → никогда, auto → по размеру сериализованных строк.
//
// len(ndjson) — близкий прокси инлайн-стоимости: строки те же, инлайн
// добавляет лишь meta и пунктуацию массива вместо переводов строки.
// InlineMaxBytes == 0 означает «фича выключена» → всегда inline.
func wantFile(delivery string, ndjsonBytes int, l Limits) bool {
	switch delivery {
	case deliveryFile:
		return true
	case deliveryInline:
		return false
	default:
		return l.InlineMaxBytes > 0 && ndjsonBytes > l.InlineMaxBytes
	}
}

// fileResult строит выход file-режима.
//
// ЖЁСТКОЕ ТРЕБОВАНИЕ: собираем НОВЫЙ schema.Result с Rows: nil — исходные
// строки в типизированный выход попасть не должны. StructuredContent
// go-sdk ставит из out безусловно, так что оставить строки в out означало
// бы не сэкономить ничего.
//
// Content заполняем сами: SDK перезаписывает его только если он nil.
func fileResult(res executeSQLOut, e spool.Entry, l Limits) (*mcp.CallToolResult, executeSQLOut, error) {
	ref := &schema.ResultRef{
		URI:      e.URI,
		MIMEType: e.MIMEType,
		Bytes:    e.Bytes,
		RowCount: e.RowCount,
		Preview:  previewRows(res.Rows, l.PreviewRows),
	}
	if l.ExposeLocalPath {
		ref.Path = e.Path
	}

	out := executeSQLOut{
		Rows:     nil,
		Meta:     res.Meta,
		Resource: ref,
	}
	out.Meta.Delivery = deliveryFile

	size := e.Bytes
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fileSummary(e, ref)},
		&mcp.ResourceLink{
			URI:      e.URI,
			Name:     "execute_sql result",
			MIMEType: e.MIMEType,
			Size:     &size,
		},
	}}, out, nil
}

// fileSummary — текст, который модель читает первым. Сформулирован так,
// чтобы она НЕ дёргала resources/read без нужды: ResourceContents.Text —
// это string, то есть чтение ресурса затянет весь файл в JSON-RPC и в
// контекст, обнулив всю экономию.
func fileSummary(e spool.Entry, ref *schema.ResultRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "execute_sql returned %d rows (%s) — too large to inline. ",
		e.RowCount, humanBytes(e.Bytes))
	fmt.Fprintf(&b, "The full result is NDJSON (one row object per line) at %s", e.URI)
	if ref.Path != "" {
		fmt.Fprintf(&b, ", local file %s", ref.Path)
	}
	b.WriteString(". structuredContent already carries meta.columns")
	if len(ref.Preview) > 0 {
		fmt.Fprintf(&b, " and resource.preview (first %d rows)", len(ref.Preview))
	}
	b.WriteString("; answer from those when you can. Read the resource ONLY if you " +
		"genuinely need every row — it would pull the whole file into the context. " +
		"Otherwise re-query with a narrower SELECT or with LIMIT/OFFSET, or process " +
		"the local file with a script.")
	return b.String()
}

// resourceMeta — то, что уедет в _meta ресурса. Без маппинга key↔name
// standalone resources/read не даёт разобрать дубликаты имён из JOIN.
func resourceMeta(res executeSQLOut) map[string]any {
	cols := make([]map[string]any, 0, len(res.Meta.Columns))
	for _, c := range res.Meta.Columns {
		col := map[string]any{"name": c.Name, "key": c.Key}
		if c.Type != "" {
			col["type"] = c.Type
		}
		cols = append(cols, col)
	}
	return map[string]any{
		"columns":   cols,
		"row_count": res.Meta.RowCount,
	}
}

// previewRows отрезает первые n строк копией: держать подслайс исходного
// массива значило бы держать в памяти весь набор ради пяти строк.
func previewRows(rows []map[string]any, n int) []map[string]any {
	if n <= 0 || len(rows) == 0 {
		return nil
	}
	n = min(n, len(rows))
	out := make([]map[string]any, n)
	copy(out, rows[:n])
	return out
}

// humanBytes — для текста summary, не для машинного парсинга
// (точное значение лежит в resource.bytes).
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// maybeAppendLimit дописывает "LIMIT <limit> OFFSET 0", если верхнеуровневого
// LIMIT'а в запросе нет. Возвращает SQL для Metabase, факты о пагинации
// ИТОГОВОГО SQL и предупреждения наружу.
//
// Если LIMIT уже есть — текст запроса не трогаем вообще. Пользовательский
// LIMIT 200000 при row_limit=1000 обрежет сам Metabase, и это станет видно
// через rows_truncated.
func maybeAppendLimit(query string, info sqlguard.Info, limit int, auto bool) (string, sqlguard.Info, []string) {
	if info.HasLimit {
		return query, info, nil
	}
	if !auto {
		return query, info, []string{fmt.Sprintf(
			"query has no top-level LIMIT and auto-limit is disabled: the result is bounded "+
				"by row_limit=%d (Metabase constraints), not by the query. "+
				"Add LIMIT/OFFSET for stable paging.", limit)}
	}

	clause := "LIMIT " + strconv.Itoa(limit) + " OFFSET 0"
	rewritten := sqlguard.WithLimitOffset(query, info, int64(limit), 0)
	// Страховка на собственный сканер хвоста (codeEnd): если вставка что-то
	// испортила, лучше уйти без LIMIT'а с предупреждением, чем отправить
	// в чужую БД битый SQL.
	page, err := sqlguard.Inspect(rewritten)
	if err != nil || !page.HasLimit {
		return query, info, []string{fmt.Sprintf(
			"query has no top-level LIMIT and the server could not append %q safely: the result "+
				"is bounded by row_limit=%d (Metabase constraints), not by the query. "+
				"Add LIMIT/OFFSET yourself.", clause, limit)}
	}
	return rewritten, page, []string{fmt.Sprintf(
		"query had no top-level LIMIT: server appended %q. "+
			"Pass LIMIT/OFFSET yourself to control paging; the next page is OFFSET %d.",
		clause, limit)}
}

// nextOffset — подсказка «с какого OFFSET начинается следующая страница».
// Требует литерального верхнеуровневого LIMIT'а: для "LIMIT ?" значение
// неизвестно. Это подсказка, а не доказательство, что дальше есть строки —
// сверяться нужно с meta.row_count.
func nextOffset(page sqlguard.Info) *int64 {
	if !page.HasLimit || page.Limit < 0 {
		return nil
	}
	off := page.Offset
	if !page.HasOffset || off < 0 {
		off = 0
	}
	n := off + page.Limit
	return &n
}

// truncatedWarning — текст про rows_truncated. Значение бывает и булевым,
// тогда лимит обрезки неизвестен.
func truncatedWarning(at int) string {
	if at > 0 {
		return fmt.Sprintf(
			"Metabase truncated the result at %d rows (row_limit / constraints.max-results): "+
				"rows are missing. Raise row_limit or narrow the query with LIMIT/OFFSET.", at)
	}
	return "Metabase truncated the result (row_limit / constraints.max-results): rows are missing. " +
		"Raise row_limit or narrow the query with LIMIT/OFFSET."
}

func normalizeLimit(n int) int {
	if n <= 0 {
		return defaultRowLimit
	}
	if n > maxRowLimit {
		return maxRowLimit
	}
	return n
}
