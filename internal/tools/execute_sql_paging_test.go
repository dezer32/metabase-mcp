package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callExecuteSQL вызывает tool и разбирает StructuredContent в executeSQLOut.
func callExecuteSQL(t *testing.T, d Deps, args map[string]any) (*mcp.CallToolResult, executeSQLOut) {
	t.Helper()
	sess := startInMem(t, d)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "execute_sql",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	var out executeSQLOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (raw=%s)", err, raw)
	}
	return res, out
}

// depsWithLimits — Deps с боевыми Limits (Limits{} = «фича выключена»,
// поэтому дефолты нужно проверять отдельно — см. риск №5 плана).
func depsWithLimits(mb MetabaseClient, l Limits) Deps {
	d := newDeps(mb)
	d.Limits = l
	return d
}

func okDataset() *metabase.DatasetResponse {
	return &metabase.DatasetResponse{
		Status:      "completed",
		RunningTime: 5,
		Data: metabase.DatasetData{
			Cols: []metabase.DatasetCol{{Name: "n", BaseType: "type/Integer"}},
			Rows: [][]any{{1}, {2}},
		},
	}
}

func TestExecuteSQL_AppendsLimitWhenMissing(t *testing.T) {
	var seen datasetCapture
	mb := &capturingMB{capture: &seen, dataset: okDataset()}
	d := depsWithLimits(mb, DefaultLimits())

	_, out := callExecuteSQL(t, d, map[string]any{
		"database_id": 3,
		"query":       "SELECT n FROM t",
	})

	if want := "SELECT n FROM t LIMIT 1000 OFFSET 0"; seen.q != want {
		t.Errorf("Metabase got %q, want %q", seen.q, want)
	}
	if out.Meta.EffectiveSQL != seen.q {
		t.Errorf("effective_sql = %q, want %q", out.Meta.EffectiveSQL, seen.q)
	}
	if out.Meta.NextOffset == nil || *out.Meta.NextOffset != 1000 {
		t.Errorf("next_offset = %v, want 1000", out.Meta.NextOffset)
	}
	if len(out.Meta.Warnings) != 1 || !strings.Contains(out.Meta.Warnings[0], "no top-level LIMIT") {
		t.Errorf("warnings = %#v", out.Meta.Warnings)
	}
	// row_limit по-прежнему уходит в constraints.
	if seen.limit != 1000 {
		t.Errorf("row_limit passed to Metabase = %d, want 1000", seen.limit)
	}
}

func TestExecuteSQL_AppendedLimitUsesRowLimit(t *testing.T) {
	var seen datasetCapture
	mb := &capturingMB{capture: &seen, dataset: okDataset()}
	d := depsWithLimits(mb, DefaultLimits())

	_, out := callExecuteSQL(t, d, map[string]any{
		"database_id": 3,
		"query":       "SELECT n FROM t;",
		"row_limit":   25,
	})

	// Вставка идёт ПЕРЕД ';', а не в конец строки.
	if want := "SELECT n FROM t LIMIT 25 OFFSET 0;"; seen.q != want {
		t.Errorf("Metabase got %q, want %q", seen.q, want)
	}
	if out.Meta.NextOffset == nil || *out.Meta.NextOffset != 25 {
		t.Errorf("next_offset = %v, want 25", out.Meta.NextOffset)
	}
}

func TestExecuteSQL_KeepsUserLimitOffset(t *testing.T) {
	var seen datasetCapture
	mb := &capturingMB{capture: &seen, dataset: okDataset()}
	d := depsWithLimits(mb, DefaultLimits())

	const q = "SELECT n FROM t LIMIT 5 OFFSET 10"
	_, out := callExecuteSQL(t, d, map[string]any{
		"database_id": 3,
		"query":       q,
	})

	if seen.q != q {
		t.Errorf("SQL был изменён: %q", seen.q)
	}
	if out.Meta.EffectiveSQL != "" {
		t.Errorf("effective_sql должен быть пуст, got %q", out.Meta.EffectiveSQL)
	}
	if len(out.Meta.Warnings) != 0 {
		t.Errorf("предупреждений быть не должно: %#v", out.Meta.Warnings)
	}
	if out.Meta.NextOffset == nil || *out.Meta.NextOffset != 15 {
		t.Errorf("next_offset = %v, want 15", out.Meta.NextOffset)
	}
}

func TestExecuteSQL_AutoLimitDisabled(t *testing.T) {
	var seen datasetCapture
	mb := &capturingMB{capture: &seen, dataset: okDataset()}
	l := DefaultLimits()
	l.AutoLimit = false
	d := depsWithLimits(mb, l)

	_, out := callExecuteSQL(t, d, map[string]any{
		"database_id": 3,
		"query":       "SELECT n FROM t",
	})

	if seen.q != "SELECT n FROM t" {
		t.Errorf("SQL был изменён при выключенном auto-limit: %q", seen.q)
	}
	if out.Meta.EffectiveSQL != "" {
		t.Errorf("effective_sql должен быть пуст, got %q", out.Meta.EffectiveSQL)
	}
	if out.Meta.NextOffset != nil {
		t.Errorf("next_offset без LIMIT'а неизвестен, got %v", *out.Meta.NextOffset)
	}
	if len(out.Meta.Warnings) != 1 || !strings.Contains(out.Meta.Warnings[0], "auto-limit is disabled") {
		t.Errorf("warnings = %#v", out.Meta.Warnings)
	}
}

func TestExecuteSQL_ParamMarkerLimitHasNoNextOffset(t *testing.T) {
	mb := &capturingMB{capture: &datasetCapture{}, dataset: okDataset()}
	d := depsWithLimits(mb, DefaultLimits())

	_, out := callExecuteSQL(t, d, map[string]any{
		"database_id": 3,
		"query":       "SELECT n FROM t LIMIT ?",
	})
	if out.Meta.NextOffset != nil {
		t.Errorf("для LIMIT ? значение неизвестно, got %v", *out.Meta.NextOffset)
	}
}

func TestExecuteSQL_ReportsTruncation(t *testing.T) {
	ds := okDataset()
	ds.Data.RowsTruncated = json.RawMessage(`2000`)
	mb := &capturingMB{capture: &datasetCapture{}, dataset: ds}
	d := depsWithLimits(mb, DefaultLimits())

	_, out := callExecuteSQL(t, d, map[string]any{
		"database_id": 3,
		"query":       "SELECT n FROM t LIMIT 50000",
	})

	if !out.Meta.Truncated || out.Meta.TruncatedAt != 2000 {
		t.Errorf("truncated = %v, truncated_at = %d", out.Meta.Truncated, out.Meta.TruncatedAt)
	}
	found := false
	for _, w := range out.Meta.Warnings {
		if strings.Contains(w, "truncated the result at 2000 rows") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %#v", out.Meta.Warnings)
	}
}

// Limits{} — «фича выключена»: SQL не переписываем, предупреждений нет.
// Именно в таком виде Deps собирают старые тесты.
func TestExecuteSQL_ZeroLimitsKeepsQueryIntact(t *testing.T) {
	var seen datasetCapture
	mb := &capturingMB{capture: &seen, dataset: okDataset()}

	_, out := callExecuteSQL(t, newDeps(mb), map[string]any{
		"database_id": 3,
		"query":       "SELECT n FROM t",
	})
	if seen.q != "SELECT n FROM t" {
		t.Errorf("SQL был изменён: %q", seen.q)
	}
	if out.Meta.Delivery != deliveryInline {
		t.Errorf("delivery = %q, want %q", out.Meta.Delivery, deliveryInline)
	}
}
