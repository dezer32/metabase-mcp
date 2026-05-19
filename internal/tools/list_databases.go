package tools

import (
	"context"
	"fmt"

	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listDatabasesIn struct{}

type DatabaseInfo struct {
	ID          int    `json:"id" jsonschema:"identifier used by other tools"`
	Name        string `json:"name" jsonschema:"human-readable name (e.g. meta_helpdesk, hydra)"`
	Engine      string `json:"engine,omitempty" jsonschema:"database engine (mysql, postgres, ...)"`
	Description string `json:"description,omitempty"`
}

type listDatabasesOut struct {
	Databases []DatabaseInfo `json:"databases"`
}

const listDatabasesDesc = "Returns connected databases visible to this Metabase. " +
	"Use this FIRST to learn which database_id corresponds to which logical source. " +
	"Cached 5 minutes."

// databasesCacheKey — Metabase возвращает один полный список без параметров,
// поэтому весь кэш — это одна запись.
const databasesCacheKey = "all"

func registerListDatabases(server *mcp.Server, d Deps) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_databases",
			Description: listDatabasesDesc,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ listDatabasesIn) (*mcp.CallToolResult, listDatabasesOut, error) {
			dbs, err := fetchDatabases(ctx, d)
			if err != nil {
				return nil, listDatabasesOut{}, fmt.Errorf("list_databases: %w", err)
			}
			out := listDatabasesOut{Databases: make([]DatabaseInfo, len(dbs))}
			for i, db := range dbs {
				out.Databases[i] = DatabaseInfo{
					ID:          db.ID,
					Name:        db.Name,
					Engine:      db.Engine,
					Description: db.Description,
				}
			}
			return nil, out, nil
		},
	)
}

func fetchDatabases(ctx context.Context, d Deps) ([]metabase.Database, error) {
	if v, ok := d.Databases.Get(databasesCacheKey); ok {
		return v, nil
	}
	dbs, err := d.MB.Databases(ctx)
	if err != nil {
		return nil, err
	}
	d.Databases.Set(databasesCacheKey, dbs)
	return dbs, nil
}
