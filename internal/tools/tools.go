package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register регистрирует все tool'ы Metabase-MCP на сервере, а также шаблон
// ресурса для спуленных результатов execute_sql.
//
// Вызывать обязательно ДО srv.Run: capability resources вычисляется один
// раз на initialize по наличию шаблона.
func Register(server *mcp.Server, d Deps) {
	registerListDatabases(server, d)
	registerListTables(server, d)
	registerExecuteSQL(server, d)
	registerResultResource(server, d)
}
