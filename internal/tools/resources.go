package tools

import (
	"context"
	"time"

	"github.com/dezer32/metabase-mcp/internal/spool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const resultResourceDesc = "Full result of an execute_sql call that was too large to inline. " +
	"NDJSON: one JSON object per line, keys are the deduplicated column keys. " +
	"The key-to-original-name mapping and the row count are in _meta. " +
	"Read this only when you really need every row: the tool response already " +
	"carries meta.columns and a preview."

// resultTemplate — шаблон ресурса metabase://result/{id}. Из него же
// resultLink собирает ResourceLink в ответе execute_sql, чтобы подписи и
// аннотации шаблона и ссылки не разъезжались. Новый объект на каждый
// вызов: ссылка не делит указатели с шаблоном, который хранит SDK.
func resultTemplate() *mcp.ResourceTemplate {
	return &mcp.ResourceTemplate{
		Name:        "execute_sql result",
		Title:       "execute_sql result (NDJSON)",
		Description: resultResourceDesc,
		MIMEType:    spool.MIMEType,
		URITemplate: spool.URITemplate,
		// Данные для модели, а не для показа пользователю. Приоритет
		// низкий: полный набор модели нужен редко, ответ tool'а уже несёт
		// meta.columns и превью. Ноль точнее, но Priority помечен
		// omitempty и до провода не дошёл бы.
		Annotations: &mcp.Annotations{
			Audience: []mcp.Role{"assistant"},
			Priority: 0.1,
		},
	}
}

// resultLink — ResourceLink на спуленный результат: подписи и аннотации
// шаблона плюс то, что известно только про этот результат. lastModified —
// только у ссылки: у шаблона одного момента изменения нет.
func resultLink(e spool.Entry) *mcp.ResourceLink {
	t := resultTemplate()
	a := *t.Annotations
	a.LastModified = e.CreatedAt.UTC().Format(time.RFC3339)
	size := e.Bytes
	return &mcp.ResourceLink{
		URI:         e.URI,
		Name:        t.Name,
		Title:       t.Title,
		MIMEType:    e.MIMEType,
		Size:        &size,
		Annotations: &a,
	}
}

// registerResultResource регистрирует шаблон ресурса metabase://result/{id}.
//
// Именно шаблон, а не AddResource на каждый результат:
//   - capability resources сервер вычисляет на рукопожатии (initialize или
//     server/discover) по наличию ресурсов/шаблонов (go-sdk mcp/server.go,
//     Server.capabilities), поэтому регистрировать надо ДО srv.Run — позже
//     клиент шаблон не увидит;
//   - featureSet сервера не растёт неограниченно;
//   - нет шторма resources/list_changed (changeAndNotify дебаунсит 10 мс).
//
// Конкретный URI клиент и так получает из ResourceLink в ответе tool'а.
func registerResultResource(server *mcp.Server, d Deps) {
	if d.Spool == nil {
		return
	}
	server.AddResourceTemplate(
		resultTemplate(),
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			// Промах, истёкший TTL и битый URI дают клиенту один и тот же
			// ответ: существовал ли когда-то такой id — не его дело.
			id, err := spool.ParseURI(req.Params.URI)
			if err != nil {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			data, e, err := d.Spool.ReadAll(id)
			if err != nil {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			return &mcp.ReadResourceResult{
				// Содержимое под id неизменно, поэтому кэшировать его можно
				// до истечения записи спула: остаток округляется вниз, для
				// прошедшего срока — 0. private: данные получены под
				// учёткой пользователя, посредникам и чужим клиентам они не
				// положены.
				Cacheable: mcp.Cacheable{
					TTLMs:      int(max(0, time.Until(e.ExpiresAt).Milliseconds())),
					CacheScope: "private",
				},
				Contents: []*mcp.ResourceContents{{
					URI:      e.URI,
					MIMEType: e.MIMEType,
					Text:     string(data),
					// _meta: без него standalone resources/read вернул бы
					// NDJSON без маппинга key↔name, а он нужен, чтобы
					// разобрать дубликаты имён из JOIN.
					Meta: mcp.Meta(e.Meta),
				}},
			}, nil
		},
	)
}
