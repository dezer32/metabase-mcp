//go:build integration

package test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildBinary компилирует metabase-mcp во временный каталог.
// Возвращает абсолютный путь к бинарю.
func buildBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "metabase-mcp")
	// Билдим из корня репозитория (test/.. → корень).
	cmd := exec.Command("go", "build", "-o", out, "..")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, b)
	}
	return out
}

// startClient запускает бинарь metabase-mcp и подключается через CommandTransport.
func startClient(t *testing.T, fake *FakeMetabase) *mcp.ClientSession {
	t.Helper()
	binary := buildBinary(t)

	cmd := exec.Command(binary)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"METABASE_URL=" + fake.URL(),
		"METABASE_USER=ci",
		"METABASE_PASSWORD=ci",
		"LOG_LEVEL=error",
	}

	cli := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	tport := &mcp.CommandTransport{Command: cmd}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	sess, err := cli.Connect(ctx, tport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = sess.Close()
	})
	return sess
}

// readJSON — выколупывает StructuredContent ответа в типизированную структуру.
func readJSON(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal (%s): %v", raw, err)
	}
}

func TestE2E_ListDatabases(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()

	sess := startClient(t, fake)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_databases"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var out struct {
		Databases []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Engine string `json:"engine"`
		} `json:"databases"`
	}
	readJSON(t, res, &out)
	if len(out.Databases) != 2 {
		t.Fatalf("databases: %d", len(out.Databases))
	}
	if out.Databases[0].Name != "meta_helpdesk" {
		t.Errorf("dbs[0]: %+v", out.Databases[0])
	}
}

func TestE2E_ListTables(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()
	sess := startClient(t, fake)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_tables",
		Arguments: map[string]any{"database_id": 3},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var out struct {
		Tables []struct {
			Name    string `json:"name"`
			Columns []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"columns"`
		} `json:"tables"`
	}
	readJSON(t, res, &out)
	if len(out.Tables) != 1 || out.Tables[0].Name != "tickets" {
		t.Fatalf("tables: %+v", out.Tables)
	}
	if len(out.Tables[0].Columns) != 2 {
		t.Errorf("columns: %d", len(out.Tables[0].Columns))
	}
}

func TestE2E_ExecuteSQL_Success(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()
	sess := startClient(t, fake)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT n FROM dummy",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var out struct {
		Rows []map[string]any `json:"rows"`
		Meta struct {
			RowCount  int   `json:"row_count"`
			RunningMs int64 `json:"running_ms"`
		} `json:"meta"`
	}
	readJSON(t, res, &out)
	if out.Meta.RowCount != 3 {
		t.Errorf("row_count: %d", out.Meta.RowCount)
	}
	if len(out.Rows) != 3 {
		t.Errorf("rows: %d", len(out.Rows))
	}
}

func TestE2E_ExecuteSQL_RejectsDestructive(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()
	sess := startClient(t, fake)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "DROP TABLE tickets",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for DROP")
	}
}

func TestE2E_ExecuteSQL_RejectsMultiStatement(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()
	sess := startClient(t, fake)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT 1; DROP TABLE x",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for multi-statement")
	}
	// Текст ошибки должен содержать «один statement» или подобное.
	found := false
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, "statement") {
			found = true
		}
	}
	if !found {
		t.Errorf("error text should mention multi-statement, got: %+v", res.Content)
	}
}
