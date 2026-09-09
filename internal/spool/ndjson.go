package spool

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MIMEType — тип содержимого файлов спула.
const MIMEType = "application/x-ndjson"

// Encode сериализует строки результата в NDJSON: по одному JSON-объекту
// на строку файла. Такой формат читается построчно и не требует держать
// весь массив в памяти на стороне потребителя.
//
// HTML-экранирование оставлено дефолтным (как у json.Marshal в go-sdk) —
// так len(Encode(rows)) остаётся честным прокси инлайн-стоимости тех же
// строк: набор символов тот же, различаются только запятые против \n.
func Encode(rows []map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(rows) * 64)
	enc := json.NewEncoder(&buf) // Encode сам дописывает \n после объекта
	for i, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, fmt.Errorf("spool: encode row %d: %w", i, err)
		}
	}
	return buf.Bytes(), nil
}
