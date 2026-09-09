//go:build integration

package test

import (
	"context"
	"encoding/json"
	"os"
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

// startClient запускает бинарь metabase-mcp и подключается через
// CommandTransport. extraEnv переопределяет базовые переменные по имени.
func startClient(t *testing.T, fake *FakeMetabase, extraEnv ...string) *mcp.ClientSession {
	t.Helper()
	binary := buildBinary(t)

	cmd := exec.Command(binary)
	cmd.Env = mergeEnv([]string{
		"PATH=/usr/bin:/bin",
		"METABASE_URL=" + fake.URL(),
		"METABASE_USER=ci",
		"METABASE_PASSWORD=ci",
		"LOG_LEVEL=error",
		// Спул по умолчанию складывает файлы в $TMPDIR/metabase-mcp/results;
		// в тестах держим их в t.TempDir(), чтобы ничего не оставлять.
		"RESULT_SPOOL_DIR=" + t.TempDir(),
	}, extraEnv)

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

// mergeEnv накладывает overrides на base по имени переменной. Полагаться
// на дедупликацию внутри os/exec не хочется — здесь она видна явно.
func mergeEnv(base, overrides []string) []string {
	out := append([]string(nil), base...)
	for _, kv := range overrides {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		replaced := false
		for i, cur := range out {
			if n, _, _ := strings.Cut(cur, "="); n == name {
				out[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return out
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

// executeSQLOut — то же, что internal/schema.Result, но объявленный здесь:
// e2e ходит через реальный протокол и не должен зависеть от internal.
type executeSQLOut struct {
	Rows []map[string]any `json:"rows"`
	Meta struct {
		RowCount     int      `json:"row_count"`
		Delivery     string   `json:"delivery"`
		EffectiveSQL string   `json:"effective_sql"`
		NextOffset   *int64   `json:"next_offset"`
		Truncated    bool     `json:"truncated"`
		TruncatedAt  int      `json:"truncated_at"`
		Warnings     []string `json:"warnings"`
		Columns      []struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"columns"`
	} `json:"meta"`
	Resource *struct {
		URI      string           `json:"uri"`
		Path     string           `json:"path"`
		MIMEType string           `json:"mime_type"`
		Bytes    int64            `json:"bytes"`
		RowCount int              `json:"row_count"`
		Preview  []map[string]any `json:"preview"`
	} `json:"resource"`
}

func TestE2E_ExecuteSQL_AppendsLimit(t *testing.T) {
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
	var out executeSQLOut
	readJSON(t, res, &out)

	if want := "SELECT n FROM dummy LIMIT 1000 OFFSET 0"; fake.LastDatasetQuery() != want {
		t.Errorf("Metabase получил %q, want %q", fake.LastDatasetQuery(), want)
	}
	if out.Meta.EffectiveSQL != fake.LastDatasetQuery() {
		t.Errorf("effective_sql = %q", out.Meta.EffectiveSQL)
	}
	if out.Meta.NextOffset == nil || *out.Meta.NextOffset != 1000 {
		t.Errorf("next_offset = %v, want 1000", out.Meta.NextOffset)
	}
	if len(out.Meta.Warnings) == 0 || !strings.Contains(out.Meta.Warnings[0], "no top-level LIMIT") {
		t.Errorf("warnings = %#v", out.Meta.Warnings)
	}
	// row_limit по-прежнему уходит в constraints.
	if got := fake.LastDatasetConstraints()["max-results"]; got != float64(1000) {
		t.Errorf("constraints.max-results = %v", got)
	}
}

func TestE2E_ExecuteSQL_KeepsUserPaging(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()
	sess := startClient(t, fake)

	const q = "SELECT n FROM dummy LIMIT 5 OFFSET 10"
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "execute_sql",
		Arguments: map[string]any{"database_id": 3, "query": q},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out executeSQLOut
	readJSON(t, res, &out)

	if fake.LastDatasetQuery() != q {
		t.Errorf("SQL был изменён: %q", fake.LastDatasetQuery())
	}
	if out.Meta.EffectiveSQL != "" {
		t.Errorf("effective_sql должен быть пуст: %q", out.Meta.EffectiveSQL)
	}
	if len(out.Meta.Warnings) != 0 {
		t.Errorf("предупреждений быть не должно: %#v", out.Meta.Warnings)
	}
	if out.Meta.NextOffset == nil || *out.Meta.NextOffset != 15 {
		t.Errorf("next_offset = %v, want 15", out.Meta.NextOffset)
	}
}

func TestE2E_ExecuteSQL_ReportsTruncation(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()
	fake.SetDatasetTruncatedAt(1000)
	sess := startClient(t, fake)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "execute_sql",
		Arguments: map[string]any{"database_id": 3, "query": "SELECT n FROM dummy LIMIT 5000"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out executeSQLOut
	readJSON(t, res, &out)

	if !out.Meta.Truncated || out.Meta.TruncatedAt != 1000 {
		t.Errorf("truncated = %v, truncated_at = %d", out.Meta.Truncated, out.Meta.TruncatedAt)
	}
}

func TestE2E_ExecuteSQL_SpoolsLargeResult(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()
	fake.SetDatasetRows(20000)

	spoolDir := t.TempDir()
	sess := startClient(t, fake,
		"RESULT_INLINE_MAX_BYTES=1024",
		"RESULT_SPOOL_DIR="+spoolDir,
	)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT id, label FROM dummy LIMIT 20000",
			"row_limit":   50000,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out executeSQLOut
	readJSON(t, res, &out)

	if out.Rows != nil {
		t.Errorf("rows должен быть null, got %d строк", len(out.Rows))
	}
	if out.Meta.Delivery != "file" {
		t.Errorf("delivery = %q, want file", out.Meta.Delivery)
	}
	if out.Meta.RowCount != 20000 {
		t.Errorf("row_count = %d, want 20000", out.Meta.RowCount)
	}
	if out.Resource == nil {
		t.Fatal("resource должен быть заполнен")
	}
	if out.Resource.RowCount != 20000 || out.Resource.Bytes == 0 {
		t.Errorf("resource = %+v", out.Resource)
	}
	if len(out.Resource.Preview) != 5 {
		t.Errorf("preview: %d строк, want 5", len(out.Resource.Preview))
	}

	// На stdio путь к локальному файлу полезен и должен существовать.
	if out.Resource.Path == "" {
		t.Fatal("resource.path пуст на stdio-транспорте")
	}
	if filepath.Dir(out.Resource.Path) != spoolDir {
		t.Errorf("файл лежит не в RESULT_SPOOL_DIR: %q", out.Resource.Path)
	}
	fi, err := os.Stat(out.Resource.Path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", out.Resource.Path, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("права файла = %o, want 600", perm)
	}

	// В Content — текст + ResourceLink, без дубля всех строк.
	var link *mcp.ResourceLink
	for _, c := range res.Content {
		if rl, ok := c.(*mcp.ResourceLink); ok {
			link = rl
		}
	}
	if link == nil {
		t.Fatalf("в Content должен быть ResourceLink: %+v", res.Content)
	}
	if link.URI != out.Resource.URI {
		t.Errorf("ResourceLink.URI = %q, want %q", link.URI, out.Resource.URI)
	}

	// resources/read по этому URI отдаёт весь набор.
	rr, err := sess.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: out.Resource.URI})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(rr.Contents) != 1 {
		t.Fatalf("contents: %d", len(rr.Contents))
	}
	if got := strings.Count(rr.Contents[0].Text, "\n"); got != 20000 {
		t.Errorf("в ресурсе %d строк, want 20000", got)
	}
	if rr.Contents[0].MIMEType != "application/x-ndjson" {
		t.Errorf("mimeType = %q", rr.Contents[0].MIMEType)
	}
	if rr.Contents[0].Meta["columns"] == nil {
		t.Errorf("_meta должен нести columns: %#v", rr.Contents[0].Meta)
	}

	// Битый URI — not found, а не путь наружу.
	if _, err := sess.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "metabase://result/../../etc/passwd"}); err == nil {
		t.Error("path traversal должен отвергаться")
	}
}

func TestE2E_ExecuteSQL_SpoolDisabled(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()
	fake.SetDatasetRows(2000)
	sess := startClient(t, fake,
		"RESULT_INLINE_MAX_BYTES=1024",
		"RESULT_SPOOL_ENABLED=false",
	)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "execute_sql",
		Arguments: map[string]any{"database_id": 3, "query": "SELECT id FROM dummy LIMIT 2000"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var out executeSQLOut
	readJSON(t, res, &out)

	if len(out.Rows) != 2000 {
		t.Errorf("rows: %d, want 2000 (inline)", len(out.Rows))
	}
	if out.Meta.Delivery != "inline" {
		t.Errorf("delivery = %q, want inline", out.Meta.Delivery)
	}
	if out.Resource != nil {
		t.Errorf("resource должен быть пуст: %+v", out.Resource)
	}
}
