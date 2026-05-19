package tools

import (
	"context"
	"fmt"

	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/dezer32/metabase-mcp/internal/schema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listTablesIn struct {
	DatabaseID int `json:"database_id" jsonschema:"id of a database returned by list_databases"`
}

type listTablesOut struct {
	Tables []schema.Table `json:"tables"`
}

const listTablesDesc = "Returns schema of one database: tables, columns with types, and foreign keys. " +
	"Use this before writing SQL. Engine is reported by list_databases — adjust SQL dialect to it. " +
	"Cached 5 minutes per database_id."

func registerListTables(server *mcp.Server, d Deps) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_tables",
			Description: listTablesDesc,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in listTablesIn) (*mcp.CallToolResult, listTablesOut, error) {
			if in.DatabaseID <= 0 {
				return nil, listTablesOut{}, fmt.Errorf("list_tables: database_id must be positive (got %d)", in.DatabaseID)
			}
			raw, err := fetchMetadata(ctx, d, in.DatabaseID)
			if err != nil {
				return nil, listTablesOut{}, fmt.Errorf("list_tables: %w", err)
			}
			return nil, listTablesOut{Tables: schema.Flatten(raw)}, nil
		},
	)
}

func fetchMetadata(ctx context.Context, d Deps, dbID int) (*metabase.MetadataRaw, error) {
	if v, ok := d.Metadata.Get(dbID); ok {
		return v, nil
	}
	raw, err := d.MB.Metadata(ctx, dbID)
	if err != nil {
		return nil, err
	}
	d.Metadata.Set(dbID, raw)
	return raw, nil
}
