// Package schema конвертирует сырые ответы Metabase в lean-структуры,
// удобные для LLM. Без I/O, чистые функции.
package schema

import (
	"strings"

	"github.com/dezer32/metabase-mcp/internal/metabase"
)

// Table — урезанное представление таблицы для LLM.
type Table struct {
	Name        string       `json:"name"`
	Schema      string       `json:"schema,omitempty"`
	Description string       `json:"description,omitempty"`
	Columns     []Column     `json:"columns"`
	ForeignKeys []ForeignKey `json:"foreign_keys,omitempty"`
}

// Column — урезанная колонка.
type Column struct {
	Name         string `json:"name"`
	Type         string `json:"type,omitempty"`
	SemanticType string `json:"semantic_type,omitempty"`
	Nullable     bool   `json:"nullable"`
	Description  string `json:"description,omitempty"`
}

// ForeignKey — связь между колонками двух таблиц.
type ForeignKey struct {
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
}

// Flatten превращает MetadataRaw (большой шумный JSON) в плоский []Table.
// Всё лишнее — fingerprint, dimension_options, visibility, points_of_interest —
// выкидывается. Из FK берётся только пара (to_table, to_column).
func Flatten(raw *metabase.MetadataRaw) []Table {
	if raw == nil || len(raw.Tables) == 0 {
		return nil
	}

	// Индексируем все поля по их ID — понадобится для резолва FK.
	type fieldRef struct {
		tableName string
		colName   string
	}
	totalFields := 0
	for _, t := range raw.Tables {
		totalFields += len(t.Fields)
	}
	fields := make(map[int]fieldRef, totalFields)
	for _, t := range raw.Tables {
		for _, f := range t.Fields {
			fields[f.ID] = fieldRef{tableName: t.Name, colName: f.Name}
		}
	}

	out := make([]Table, 0, len(raw.Tables))
	for _, t := range raw.Tables {
		cols := make([]Column, 0, len(t.Fields))
		var fks []ForeignKey
		for _, f := range t.Fields {
			cols = append(cols, Column{
				Name:         f.Name,
				Type:         normalizeType(f.BaseType),
				SemanticType: normalizeType(f.SemanticType),
				Nullable:     !f.DatabaseRequired,
				Description:  f.Description,
			})
			if f.FKTargetFieldID != nil {
				if ref, ok := fields[*f.FKTargetFieldID]; ok {
					fks = append(fks, ForeignKey{
						FromColumn: f.Name,
						ToTable:    ref.tableName,
						ToColumn:   ref.colName,
					})
				}
			}
		}
		out = append(out, Table{
			Name:        t.Name,
			Schema:      t.Schema,
			Description: t.Description,
			Columns:     cols,
			ForeignKeys: fks,
		})
	}
	return out
}

// normalizeType приводит "type/Integer" → "integer", "type/DateTimeWithTZ" → "datetimewithtz".
// Пустую строку оставляет пустой.
func normalizeType(s string) string {
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "type/")
	return strings.ToLower(s)
}
