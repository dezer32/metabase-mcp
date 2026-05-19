package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecuteSQL_OK(t *testing.T) {
	mb := &fakeMB{
		dataset: &metabase.DatasetResponse{
			Status:      "completed",
			RunningTime: 13,
			Data: metabase.DatasetData{
				Cols: []metabase.DatasetCol{
					{Name: "id", BaseType: "type/Integer"},
					{Name: "name", BaseType: "type/Text"},
				},
				Rows: [][]any{{1, "alice"}, {2, "bob"}},
			},
		},
	}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT id, name FROM users",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %+v", res.Content)
	}

	raw, _ := json.Marshal(res.StructuredContent)
	var out executeSQLOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (raw=%s)", err, raw)
	}
	if out.Meta.RowCount != 2 {
		t.Errorf("RowCount: %d", out.Meta.RowCount)
	}
	if out.Meta.RunningMs != 13 {
		t.Errorf("RunningMs: %d", out.Meta.RunningMs)
	}
	if len(out.Rows) != 2 {
		t.Fatalf("rows: %d", len(out.Rows))
	}
	if got := out.Rows[0]["name"]; got != "alice" {
		t.Errorf("row 0 name: %v", got)
	}
}

func TestExecuteSQL_RejectsNonSelect(t *testing.T) {
	mb := &fakeMB{}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "DROP TABLE users",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for DROP")
	}
	if mb.datasetCalls != 0 {
		t.Errorf("Metabase should not be hit, but datasetCalls=%d", mb.datasetCalls)
	}
}

func TestExecuteSQL_RejectsMultiStatement(t *testing.T) {
	mb := &fakeMB{}
	sess := startInMem(t, newDeps(mb))

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
	if mb.datasetCalls != 0 {
		t.Errorf("Metabase should not be hit")
	}
}

func TestExecuteSQL_FailedFromMetabase(t *testing.T) {
	mb := &fakeMB{datasetErr: errors.New("metabase: query failed: table not found")}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT * FROM ghost",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError")
	}
	// Текст ошибки должен попасть в Content.
	found := false
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, "table not found") {
			found = true
		}
	}
	if !found {
		t.Errorf("error text should mention Metabase reason, got: %+v", res.Content)
	}
}

func TestExecuteSQL_BadDatabaseID(t *testing.T) {
	mb := &fakeMB{}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 0,
			"query":       "SELECT 1",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for database_id=0")
	}
}

func TestNormalizeLimit(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, defaultRowLimit},
		{-5, defaultRowLimit},
		{1, 1},
		{500, 500},
		{maxRowLimit, maxRowLimit},
		{maxRowLimit + 1, maxRowLimit},
		{1_000_000, maxRowLimit},
	}
	for _, tc := range cases {
		if got := normalizeLimit(tc.in); got != tc.want {
			t.Errorf("normalizeLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// datasetCapture — фиксирует аргументы вызова Dataset.
type datasetCapture struct {
	dbID  int
	q     string
	limit int
}

func TestExecuteSQL_PassesLimit(t *testing.T) {
	var seen datasetCapture
	mb := &capturingMB{
		capture: &seen,
		dataset: &metabase.DatasetResponse{Status: "completed"},
	}
	d := newDeps(mb)
	sess := startInMem(t, d)

	_, _ = sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 4,
			"query":       "SELECT 1",
			"row_limit":   42,
		},
	})
	if seen.dbID != 4 || seen.q != "SELECT 1" || seen.limit != 42 {
		t.Errorf("Dataset called with: %+v", seen)
	}
}

// capturingMB — мок, который сохраняет аргументы Dataset.
type capturingMB struct {
	capture *datasetCapture
	dataset *metabase.DatasetResponse
}

func (m *capturingMB) Databases(_ context.Context) ([]metabase.Database, error) {
	return nil, nil
}
func (m *capturingMB) Metadata(_ context.Context, _ int) (*metabase.MetadataRaw, error) {
	return nil, nil
}
func (m *capturingMB) Dataset(_ context.Context, id int, q string, limit int) (*metabase.DatasetResponse, error) {
	m.capture.dbID = id
	m.capture.q = q
	m.capture.limit = limit
	return m.dataset, nil
}
