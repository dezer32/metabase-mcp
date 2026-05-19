package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dezer32/metabase-mcp/internal/cache"
	"github.com/dezer32/metabase-mcp/internal/logging"
	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeMB — in-memory мок MetabaseClient. Считает вызовы Databases.
type fakeMB struct {
	databases      []metabase.Database
	databasesErr   error
	databasesCalls int

	meta      *metabase.MetadataRaw
	metaErr   error
	metaCalls int

	dataset      *metabase.DatasetResponse
	datasetErr   error
	datasetCalls int
}

func (f *fakeMB) Databases(_ context.Context) ([]metabase.Database, error) {
	f.databasesCalls++
	if f.databasesErr != nil {
		return nil, f.databasesErr
	}
	return f.databases, nil
}

func (f *fakeMB) Metadata(_ context.Context, _ int) (*metabase.MetadataRaw, error) {
	f.metaCalls++
	return f.meta, f.metaErr
}

func (f *fakeMB) Dataset(_ context.Context, _ int, _ string, _ int) (*metabase.DatasetResponse, error) {
	f.datasetCalls++
	return f.dataset, f.datasetErr
}

// startInMem поднимает MCP-сервер с заданными deps на in-memory транспорте
// и возвращает client-сессию для вызова tool'ов.
func startInMem(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()

	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	Register(srv, d)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- srv.Run(context.Background(), st)
	}()

	cl := mcp.NewClient(&mcp.Implementation{Name: "test-cli", Version: "0"}, nil)
	sess, err := cl.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = sess.Close()
		select {
		case <-serverErrCh:
		case <-time.After(2 * time.Second):
			t.Log("server did not exit cleanly within 2s")
		}
	})
	return sess
}

func newDeps(mb MetabaseClient) Deps {
	return Deps{
		MB:        mb,
		Databases: cache.New[string, []metabase.Database](5 * time.Minute),
		Metadata:  cache.New[int, *metabase.MetadataRaw](5 * time.Minute),
		Log:       logging.Discard(),
	}
}

func TestListDatabases_OK(t *testing.T) {
	mb := &fakeMB{databases: []metabase.Database{
		{ID: 3, Name: "meta_helpdesk", Engine: "mysql"},
		{ID: 5, Name: "hydra", Engine: "mysql"},
	}}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_databases",
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned IsError: %+v", res.Content)
	}

	// Парсим StructuredContent, не Content (TextContent).
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out listDatabasesOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (raw=%s)", err, raw)
	}
	if len(out.Databases) != 2 {
		t.Fatalf("databases len: %d", len(out.Databases))
	}
	if out.Databases[0].Name != "meta_helpdesk" || out.Databases[0].ID != 3 {
		t.Errorf("databases[0]: %+v", out.Databases[0])
	}
}

func TestListDatabases_Cached(t *testing.T) {
	mb := &fakeMB{databases: []metabase.Database{{ID: 1, Name: "x"}}}
	sess := startInMem(t, newDeps(mb))

	for i := 0; i < 3; i++ {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_databases"})
		if err != nil {
			t.Fatalf("call #%d: %v", i, err)
		}
		if res.IsError {
			t.Fatalf("call #%d isError: %+v", i, res.Content)
		}
	}
	if mb.databasesCalls != 1 {
		t.Errorf("expected 1 backing call due to caching, got %d", mb.databasesCalls)
	}
}

func TestListDatabases_Error(t *testing.T) {
	mb := &fakeMB{databasesErr: errors.New("boom")}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_databases"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError, got success: %+v", res.StructuredContent)
	}
}
