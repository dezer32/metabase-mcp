// Package metabase — HTTP-клиент к REST-API Metabase.
// Не знает про MCP. Может быть использован в отрыве.
package metabase

import (
	"bytes"
	"encoding/json"
)

// Database — урезанная DTO для GET /api/database.
// Metabase возвращает больше полей, парсим только то, что нужно.
type Database struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Description string `json:"description,omitempty"`
}

// listDatabasesEnvelope — Metabase оборачивает список в {"data": [...]}.
// Старые версии возвращают голый массив. Поддерживаем оба варианта.
type listDatabasesEnvelope struct {
	Data []Database `json:"data"`
}

// MetadataRaw — сырая структура GET /api/database/:id/metadata.
// Парсим только нужные подмножества: tables и их fields с FK.
type MetadataRaw struct {
	Tables []RawTable `json:"tables"`
}

// RawTable — сырая таблица из metadata.
type RawTable struct {
	ID          int        `json:"id"`
	Name        string     `json:"name"`
	Schema      string     `json:"schema"`
	Description string     `json:"description"`
	Fields      []RawField `json:"fields"`
}

// RawField — сырая колонка из metadata.
type RawField struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	BaseType         string `json:"base_type"`
	DatabaseType     string `json:"database_type"`
	SemanticType     string `json:"semantic_type"`
	Description      string `json:"description"`
	DatabaseRequired bool   `json:"database_required"`
	// fk_target_field_id указывает на field_id таблицы-родителя, если
	// эта колонка является внешним ключом. Используем pointer, чтобы
	// отличать «нет FK» от «нулевой ID» (теоретически возможно).
	FKTargetFieldID *int `json:"fk_target_field_id"`
}

// DatasetResponse — ответ POST /api/dataset.
// Поле error не имеет строгой формы (string|object|nil) — поэтому RawMessage.
type DatasetResponse struct {
	Status      string           `json:"status"`
	Error       json.RawMessage  `json:"error,omitempty"`
	ErrorType   string           `json:"error_type,omitempty"`
	Class       string           `json:"class,omitempty"`
	Via         []map[string]any `json:"via,omitempty"`
	RunningTime int64            `json:"running_time"`
	Data        DatasetData      `json:"data"`
}

// DatasetData — успешные данные ответа.
type DatasetData struct {
	Cols []DatasetCol `json:"cols"`
	Rows [][]any      `json:"rows"`
	// RowsTruncated — сигнал самого Metabase о том, что результат обрезали
	// по constraints. Ключ лежит под data, значение — integer, равный лимиту
	// обрезки (query_processor/middleware/add_rows_truncated.clj:
	// (assoc-in [:data truncated-key] limit)); при отсутствии обрезания
	// ключа нет вовсе.
	//
	// Тип RawMessage, а НЕ *int: если какая-то версия отдаст true,
	// json.Unmarshal в *int вернёт ошибку, и doJSON уронит весь успешный
	// запрос. Слишком дорого за поле-предупреждение. Тот же приём уже
	// применён к Error выше.
	RowsTruncated json.RawMessage `json:"rows_truncated,omitempty"`
}

// Truncated интерпретирует rows_truncated терпимо: число → (n, true),
// true → (0, true), отсутствие/false/мусор → (0, false).
func (d *DatasetData) Truncated() (int, bool) {
	if d == nil {
		return 0, false
	}
	raw := bytes.TrimSpace(d.RowsTruncated)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return 0, b
	}
	return 0, false
}

// DatasetCol — колонка результата запроса.
type DatasetCol struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	BaseType     string `json:"base_type"`
	SemanticType string `json:"semantic_type"`
}
