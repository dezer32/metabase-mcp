package tools

import (
	"context"
	"fmt"

	"github.com/dezer32/metabase-mcp/internal/schema"
	"github.com/dezer32/metabase-mcp/internal/sqlguard"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type executeSQLIn struct {
	DatabaseID int    `json:"database_id" jsonschema:"id of a database returned by list_databases"`
	Query      string `json:"query" jsonschema:"SQL query to execute; only SELECT and WITH allowed"`
	RowLimit   int    `json:"row_limit,omitempty" jsonschema:"max rows returned (default 1000, max 50000)"`
}

type executeSQLOut = schema.Result

const (
	defaultRowLimit = 1000
	maxRowLimit     = 50000
)

const executeSQLDesc = "Executes a read-only SQL query against a database via Metabase. " +
	"Only SELECT and WITH (CTE) are allowed. " +
	"Use the engine from list_databases to pick the right SQL dialect. " +
	"Returns rows as objects with column names. " +
	"Parameters: database_id (int, from list_databases), query (string), " +
	"row_limit (int, optional, default 1000, max 50000)."

func registerExecuteSQL(server *mcp.Server, d Deps) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "execute_sql",
			Description: executeSQLDesc,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in executeSQLIn) (*mcp.CallToolResult, executeSQLOut, error) {
			if in.DatabaseID <= 0 {
				return nil, executeSQLOut{}, fmt.Errorf("execute_sql: database_id must be positive (got %d)", in.DatabaseID)
			}
			// Валидация ДО Metabase: блокируем DROP, INSERT, multi-statement и т.п.
			if err := sqlguard.Validate(in.Query); err != nil {
				return nil, executeSQLOut{}, fmt.Errorf("execute_sql: %w", err)
			}
			limit := normalizeLimit(in.RowLimit)

			resp, err := d.MB.Dataset(ctx, in.DatabaseID, in.Query, limit)
			if err != nil {
				return nil, executeSQLOut{}, fmt.Errorf("execute_sql: %w", err)
			}
			return nil, schema.Rows(resp.Data.Cols, resp.Data.Rows, resp.RunningTime), nil
		},
	)
}

func normalizeLimit(n int) int {
	if n <= 0 {
		return defaultRowLimit
	}
	if n > maxRowLimit {
		return maxRowLimit
	}
	return n
}
