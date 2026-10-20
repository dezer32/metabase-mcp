package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register регистрирует все tool'ы Metabase-MCP на сервере, а также шаблон
// ресурса для спуленных результатов execute_sql.
//
// Вызывать обязательно ДО srv.Run: capability resources вычисляется на
// рукопожатии (initialize или server/discover) по наличию шаблона.
func Register(server *mcp.Server, d Deps) {
	registerListDatabases(server, d)
	registerListTables(server, d)
	registerExecuteSQL(server, d)
	registerResultResource(server, d)
}

// readOnlyTool собирает описание tool'а с read-only хинтами. Сервер
// read-only по построению, поэтому хинты задаются здесь, в одном месте:
// tool, зарегистрированный в обход хелпера, уронит
// TestTools_ReadOnlyAnnotations.
//
// Без аннотаций хост видит дефолты спецификации — readOnlyHint=false,
// destructiveHint=true, openWorldHint=true, — то есть «может разрушить»,
// и лишний раз спрашивает подтверждение.
func readOnlyTool(name, title, desc string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Title:       title,
		Description: desc,
		Annotations: &mcp.ToolAnnotations{
			// Дубль top-level title: клиенты спецификации 2025-03-26 знают
			// только этот.
			Title:           title,
			ReadOnlyHint:    true,
			DestructiveHint: new(false),
			// Спул — внутренний кэш, а не окружение: повторный вызов
			// снаружи ничего не меняет.
			IdempotentHint: true,
			// Домен закрыт: один сконфигурированный Metabase.
			OpenWorldHint: new(false),
		},
	}
}
