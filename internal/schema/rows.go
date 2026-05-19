package schema

import (
	"strconv"

	"github.com/dezer32/metabase-mcp/internal/metabase"
)

// Result — выходной формат для tool execute_sql.
// Rows — массив объектов (по строке на запись).
// Meta содержит счётчики и оригинальные имена столбцов (важно для JOIN-ов
// с дубликатами имён).
type Result struct {
	Rows []map[string]any `json:"rows"`
	Meta Meta             `json:"meta"`
}

// Meta — метаданные результата.
type Meta struct {
	RowCount  int          `json:"row_count"`
	RunningMs int64        `json:"running_ms"`
	Columns   []ColumnMeta `json:"columns"`
}

// ColumnMeta — описание одной колонки результата.
// Name — оригинальное имя (как в SQL), может повторяться.
// Key — уникализированный ключ в Rows[i] (id, id_2, id_3 ...).
// Type — нормализованный base_type Metabase.
type ColumnMeta struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	Type string `json:"type,omitempty"`
}

// Rows конвертирует пару (cols, raw) в Result.
// Дедуплицирует имена колонок: первое вхождение остаётся, последующие
// получают суффикс _2, _3 и т.д. Иначе map[string]any потерял бы данные.
func Rows(cols []metabase.DatasetCol, raw [][]any, runningMs int64) Result {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	keys := uniquify(names)

	out := make([]map[string]any, len(raw))
	for i, row := range raw {
		m := make(map[string]any, len(cols))
		for j, k := range keys {
			if j < len(row) {
				m[k] = row[j]
			}
		}
		out[i] = m
	}

	metaCols := make([]ColumnMeta, len(cols))
	for i, c := range cols {
		metaCols[i] = ColumnMeta{
			Name: c.Name,
			Key:  keys[i],
			Type: normalizeType(c.BaseType),
		}
	}

	return Result{
		Rows: out,
		Meta: Meta{
			RowCount:  len(raw),
			RunningMs: runningMs,
			Columns:   metaCols,
		},
	}
}

// uniquify: первое вхождение остаётся как есть, последующие получают
// суффикс _2, _3, ... — детерминированно слева направо.
func uniquify(names []string) []string {
	seen := make(map[string]int, len(names))
	out := make([]string, len(names))
	for i, n := range names {
		count := seen[n]
		seen[n] = count + 1
		if count == 0 {
			out[i] = n
		} else {
			out[i] = n + "_" + strconv.Itoa(count+1)
		}
	}
	return out
}
