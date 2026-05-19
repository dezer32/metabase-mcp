package metabase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrQueryFailed — sentinel-ошибка, в которую заворачивается любой не-completed
// статус ответа /api/dataset. Полезно различать «Metabase отказал в запросе»
// от «не смогли достучаться до Metabase».
var ErrQueryFailed = errors.New("metabase: query failed")

// statusCompleted — единственный «успешный» статус /api/dataset.
const statusCompleted = "completed"

// Dataset выполняет native-запрос к указанной БД и возвращает результат.
//
// КРИТИЧНО:
//   - Metabase отвечает 200 OK даже на failed-запросы. Поэтому HTTP-код
//     проверяем как «любой 2xx», а реальный успех/провал — по полю status.
//   - Если status != "completed", извлекаем человекочитаемый текст ошибки
//     по цепочке: error (string|object.message|object.cause) → via[0].message
//     → fallback.
func (c *Client) Dataset(ctx context.Context, databaseID int, query string, rowLimit int) (*DatasetResponse, error) {
	body := map[string]any{
		"database": databaseID,
		"type":     "native",
		// template-tags обязательны в новых версиях Metabase, в старых —
		// игнорируются. Передаём пустой объект на оба случая.
		"native": map[string]any{
			"query":         query,
			"template-tags": map[string]any{},
		},
		"constraints": map[string]any{
			"max-results":           rowLimit,
			"max-results-bare-rows": rowLimit,
		},
	}

	var resp DatasetResponse
	if err := c.doJSON(ctx, "POST", "/api/dataset", body, &resp); err != nil {
		return nil, err
	}
	if resp.Status != statusCompleted {
		return nil, fmt.Errorf("%w: %s", ErrQueryFailed, extractMetabaseError(&resp))
	}
	return &resp, nil
}

// extractMetabaseError достаёт читаемый текст из ответа.
// Поле error может быть строкой, объектом, либо отсутствовать.
// Возвращает «status=...» в качестве fallback.
func extractMetabaseError(resp *DatasetResponse) string {
	if len(resp.Error) > 0 {
		// 1) Попробуем как строку.
		var s string
		if err := json.Unmarshal(resp.Error, &s); err == nil && s != "" {
			return s
		}
		// 2) Попробуем как объект, ищем message/cause.
		var obj map[string]any
		if err := json.Unmarshal(resp.Error, &obj); err == nil {
			if m, ok := obj["message"].(string); ok && m != "" {
				return m
			}
			if m, ok := obj["cause"].(string); ok && m != "" {
				return m
			}
		}
	}
	// 3) Цепочка via — Metabase складывает туда wrapped-исключения JVM.
	if len(resp.Via) > 0 {
		if m, ok := resp.Via[0]["message"].(string); ok && m != "" {
			return m
		}
	}
	// 4) Fallback.
	return fmt.Sprintf("status=%s", resp.Status)
}
