package metabase

import (
	"context"
	"encoding/json"
)

// Databases возвращает список подключённых к Metabase баз данных.
// Metabase оборачивает ответ в {"data": [...]}, но в очень старых версиях
// возвращает голый массив. Поддерживаем оба варианта.
func (c *Client) Databases(ctx context.Context) ([]Database, error) {
	var raw json.RawMessage
	if err := c.doJSON(ctx, "GET", "/api/database", nil, &raw); err != nil {
		return nil, err
	}
	// Сначала пробуем как envelope.
	var env listDatabasesEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Data != nil {
		return env.Data, nil
	}
	// Иначе — голый массив.
	var arr []Database
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}
