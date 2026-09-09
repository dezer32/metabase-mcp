package tools

import (
	"context"

	"github.com/dezer32/metabase-mcp/internal/spool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const resultResourceDesc = "Full result of an execute_sql call that was too large to inline. " +
	"NDJSON: one JSON object per line, keys are the deduplicated column keys. " +
	"The key-to-original-name mapping and the row count are in _meta. " +
	"Read this only when you really need every row: the tool response already " +
	"carries meta.columns and a preview."

// registerResultResource регистрирует шаблон ресурса metabase://result/{id}.
//
// Именно шаблон, а не AddResource на каждый результат:
//   - capability resources сервер вычисляет один раз при initialize по
//     наличию ресурсов/шаблонов (go-sdk mcp/server.go, Server.capabilities),
//     поэтому регистрировать надо ДО srv.Run — позже клиент шаблон не увидит;
//   - featureSet сервера не растёт неограниченно;
//   - нет шторма resources/list_changed (changeAndNotify дебаунсит 10 мс).
//
// Конкретный URI клиент и так получает из ResourceLink в ответе tool'а.
func registerResultResource(server *mcp.Server, d Deps) {
	if d.Spool == nil {
		return
	}
	server.AddResourceTemplate(
		&mcp.ResourceTemplate{
			Name:        "execute_sql result",
			Title:       "execute_sql result (NDJSON)",
			Description: resultResourceDesc,
			MIMEType:    spool.MIMEType,
			URITemplate: spool.URITemplate,
		},
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
