package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register регистрирует все tool'ы Metabase-MCP на сервере.
func Register(server *mcp.Server, d Deps) {
	registerListDatabases(server, d)
	registerListTables(server, d)
	registerExecuteSQL(server, d)
}
