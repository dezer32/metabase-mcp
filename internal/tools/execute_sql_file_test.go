package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/dezer32/metabase-mcp/internal/spool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeSpool — мок Spool: держит один результат в памяти.
type fakeSpool struct {
	putCalls int
	putErr   error

	data []byte
	meta map[string]any
	rows int
}

func (f *fakeSpool) Put(ndjson []byte, rowCount int, meta map[string]any) (spool.Entry, error) {
	f.putCalls++
	if f.putErr != nil {
		return spool.Entry{}, f.putErr
	}
	f.data = ndjson
	f.meta = meta
	f.rows = rowCount
	const id = "0123456789abcdef"
	return spool.Entry{
		ID:       id,
		URI:      spool.URIFor(id),
		Path:     "/tmp/metabase-mcp/results/" + id + ".ndjson",
		MIMEType: spool.MIMEType,
		Bytes:    int64(len(ndjson)),
		RowCount: rowCount,
		Meta:     meta,
	}, nil
}

func (f *fakeSpool) ReadAll(id string) ([]byte, spool.Entry, error) {
	if f.data == nil || id != "0123456789abcdef" {
		return nil, spool.Entry{}, spool.ErrNotFound
	}
	return f.data, spool.Entry{
		ID:       id,
		URI:      spool.URIFor(id),
		MIMEType: spool.MIMEType,
		Bytes:    int64(len(f.data)),
		RowCount: f.rows,
		Meta:     f.meta,
	}, nil
}

// bigDataset — n строк с маркером в 50-й (индекс 49).
func bigDataset(n int, marker string) *metabase.DatasetResponse {
	rows := make([][]any, n)
	for i := range rows {
		v := fmt.Sprintf("row-%d", i)
		if i == 49 {
			v = marker
		}
		rows[i] = []any{i, v}
	}
	return &metabase.DatasetResponse{
		Status:      "completed",
		RunningTime: 11,
		Data: metabase.DatasetData{
			Cols: []metabase.DatasetCol{
				{Name: "id", BaseType: "type/Integer"},
				{Name: "id", BaseType: "type/Text"}, // дубликат имени → id_2
			},
			Rows: rows,
		},
	}
}

func fileModeDeps(mb MetabaseClient, sp Spool) Deps {
	l := DefaultLimits()
	l.InlineMaxBytes = 512 // порог занижен, чтобы file-режим сработал
	l.ExposeLocalPath = true
	d := depsWithLimits(mb, l)
	d.Spool = sp
	return d
}

// TestExecuteSQL_FileMode_NoRowsInStructuredContent — страж главного
// требования file-режима: ни одна строка результата не должна оказаться
// в structuredContent.
func TestExecuteSQL_FileMode_NoRowsInStructuredContent(t *testing.T) {
	const marker = "MARKER-a7f3c1"
	sp := &fakeSpool{}
	mb := &capturingMB{capture: &datasetCapture{}, dataset: bigDataset(200, marker)}

	sess := startInMem(t, fileModeDeps(mb, sp))
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT id, id FROM t LIMIT 200",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %+v", res.Content)
	}

	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), marker) {
		t.Fatalf("маркер строки №50 попал в structuredContent — экономии нет")
	}

	var out executeSQLOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Rows != nil {
		t.Errorf("rows должен быть null, got %d строк", len(out.Rows))
	}
	if out.Meta.Delivery != deliveryFile {
		t.Errorf("delivery = %q, want %q", out.Meta.Delivery, deliveryFile)
	}
	if out.Meta.RowCount != 200 {
		t.Errorf("row_count = %d, want 200", out.Meta.RowCount)
	}
	if out.Resource == nil {
		t.Fatal("resource должен быть заполнен")
	}
	if out.Resource.URI != "metabase://result/0123456789abcdef" {
		t.Errorf("resource.uri = %q", out.Resource.URI)
	}
	if out.Resource.RowCount != 200 || out.Resource.Bytes == 0 {
		t.Errorf("resource = %+v", out.Resource)
	}
	if out.Resource.Path == "" {
		t.Error("ExposeLocalPath=true → resource.path должен быть заполнен")
	}
	if len(out.Resource.Preview) != DefaultLimits().PreviewRows {
		t.Errorf("preview: %d строк, want %d", len(out.Resource.Preview), DefaultLimits().PreviewRows)
	}
	// Дедупликация имён колонок обязана сохраниться и в file-режиме.
	if len(out.Meta.Columns) != 2 || out.Meta.Columns[1].Key != "id_2" {
		t.Errorf("columns = %+v", out.Meta.Columns)
	}

	// Content мы заполняем сами: текст + ResourceLink, без дубля JSON.
	var link *mcp.ResourceLink
	for _, c := range res.Content {
		if rl, ok := c.(*mcp.ResourceLink); ok {
			link = rl
		}
	}
	if link == nil {
		t.Fatalf("в Content должен быть ResourceLink: %+v", res.Content)
	}
	if link.URI != out.Resource.URI || link.MIMEType != spool.MIMEType {
		t.Errorf("ResourceLink = %+v", link)
	}
	if link.Size == nil || *link.Size != out.Resource.Bytes {
		t.Errorf("ResourceLink.Size = %v", link.Size)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, marker) {
			t.Error("маркер попал в текстовый Content")
		}
	}

	// В файле — все строки, включая маркер.
	if got := strings.Count(string(sp.data), "\n"); got != 200 {
		t.Errorf("в NDJSON %d строк, want 200", got)
	}
	if !strings.Contains(string(sp.data), marker) {
		t.Error("маркер должен быть в файле")
	}
	// _meta ресурса несёт маппинг key↔name.
	cols, _ := sp.meta["columns"].([]map[string]any)
	if len(cols) != 2 || cols[1]["key"] != "id_2" || cols[1]["name"] != "id" {
		t.Errorf("_meta.columns = %#v", sp.meta["columns"])
	}
}

// TestExecuteSQL_SpoolPutError — спул сломался: запрос всё равно успешен,
// строки уходят инлайном, Metabase дёрнут ровно один раз.
func TestExecuteSQL_SpoolPutError(t *testing.T) {
	sp := &fakeSpool{putErr: errors.New("disk full")}
	mb := &fakeMB{dataset: bigDataset(200, "x")}

	sess := startInMem(t, fileModeDeps(mb, sp))
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT id, id FROM t LIMIT 200",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("сломанный спул не должен ронять запрос: %+v", res.Content)
	}

	raw, _ := json.Marshal(res.StructuredContent)
	var out executeSQLOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Rows) != 200 {
		t.Errorf("rows: %d, want 200 (inline)", len(out.Rows))
	}
	if out.Meta.Delivery != deliveryInline {
		t.Errorf("delivery = %q, want %q", out.Meta.Delivery, deliveryInline)
	}
	if out.Resource != nil {
		t.Errorf("resource должен быть пуст: %+v", out.Resource)
	}
	found := false
	for _, w := range out.Meta.Warnings {
		if strings.Contains(w, "could not be spooled") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %#v", out.Meta.Warnings)
	}
	if sp.putCalls != 1 {
		t.Errorf("Put вызван %d раз, want 1", sp.putCalls)
	}
	if mb.datasetCalls != 1 {
		t.Errorf("Metabase дёрнут %d раз, want 1", mb.datasetCalls)
	}
}

func TestExecuteSQL_DeliveryForcesMode(t *testing.T) {
	t.Run("file на малом результате", func(t *testing.T) {
		sp := &fakeSpool{}
		mb := &capturingMB{capture: &datasetCapture{}, dataset: okDataset()}
		_, out := callExecuteSQL(t, fileModeDeps(mb, sp), map[string]any{
			"database_id": 3,
			"query":       "SELECT n FROM t LIMIT 2",
			"delivery":    "file",
		})
		if out.Meta.Delivery != deliveryFile || out.Rows != nil {
			t.Errorf("delivery=%q rows=%d", out.Meta.Delivery, len(out.Rows))
		}
		if sp.putCalls != 1 {
			t.Errorf("Put вызван %d раз", sp.putCalls)
		}
	})

	t.Run("inline на большом результате", func(t *testing.T) {
		sp := &fakeSpool{}
		mb := &capturingMB{capture: &datasetCapture{}, dataset: bigDataset(200, "x")}
		_, out := callExecuteSQL(t, fileModeDeps(mb, sp), map[string]any{
			"database_id": 3,
			"query":       "SELECT id, id FROM t LIMIT 200",
			"delivery":    "inline",
		})
		if out.Meta.Delivery != deliveryInline || len(out.Rows) != 200 {
			t.Errorf("delivery=%q rows=%d", out.Meta.Delivery, len(out.Rows))
		}
		if sp.putCalls != 0 {
			t.Errorf("Put не должен вызываться, вызван %d раз", sp.putCalls)
		}
	})

	t.Run("file без спула — предупреждение, не ошибка", func(t *testing.T) {
		mb := &capturingMB{capture: &datasetCapture{}, dataset: okDataset()}
		d := depsWithLimits(mb, DefaultLimits()) // Spool == nil
		_, out := callExecuteSQL(t, d, map[string]any{
			"database_id": 3,
			"query":       "SELECT n FROM t LIMIT 2",
			"delivery":    "file",
		})
		if out.Meta.Delivery != deliveryInline || len(out.Rows) != 2 {
			t.Errorf("delivery=%q rows=%d", out.Meta.Delivery, len(out.Rows))
		}
		found := false
		for _, w := range out.Meta.Warnings {
			if strings.Contains(w, "spool is disabled") {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings = %#v", out.Meta.Warnings)
		}
	})
}

func TestExecuteSQL_BadDelivery(t *testing.T) {
	mb := &fakeMB{}
	sess := startInMem(t, fileModeDeps(mb, &fakeSpool{}))
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT 1",
			"delivery":    "carrier-pigeon",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("ожидалась ошибка для delivery=carrier-pigeon")
	}
	if mb.datasetCalls != 0 {
		t.Errorf("Metabase не должен быть дёрнут, datasetCalls=%d", mb.datasetCalls)
	}
}

// Ресурс отдаёт весь набор и _meta; неизвестный/битый URI — not found.
func TestResultResource_Read(t *testing.T) {
	sp := &fakeSpool{}
	mb := &capturingMB{capture: &datasetCapture{}, dataset: bigDataset(200, "MARK")}
	sess := startInMem(t, fileModeDeps(mb, sp))

	_, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT id, id FROM t LIMIT 200",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	rr, err := sess.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "metabase://result/0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(rr.Contents) != 1 {
		t.Fatalf("contents: %d", len(rr.Contents))
	}
	c := rr.Contents[0]
	if c.MIMEType != spool.MIMEType {
		t.Errorf("mimeType = %q", c.MIMEType)
	}
	if got := strings.Count(c.Text, "\n"); got != 200 {
		t.Errorf("в ресурсе %d строк, want 200", got)
	}
	if !strings.Contains(c.Text, "MARK") {
		t.Error("ресурс должен содержать все строки")
	}
	if c.Meta == nil || c.Meta["columns"] == nil {
		t.Errorf("_meta должен нести columns: %#v", c.Meta)
	}

	// Шаблон обязан быть виден в resources/templates/list.
	lst, err := sess.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if len(lst.ResourceTemplates) != 1 || lst.ResourceTemplates[0].URITemplate != spool.URITemplate {
		t.Errorf("templates = %+v", lst.ResourceTemplates)
	}
}

func TestResultResource_NotFound(t *testing.T) {
	sess := startInMem(t, fileModeDeps(&fakeMB{}, &fakeSpool{}))
	for _, uri := range []string{
		"metabase://result/ffffffffffffffff",     // валидный id, но пусто
		"metabase://result/../../etc/passwd",     // path traversal
		"metabase://result/not-hex-at-all-here!", // мусор
	} {
		if _, err := sess.ReadResource(context.Background(),
			&mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("ReadResource(%q) = nil, ожидалась ошибка", uri)
		}
	}
}

// Без спула шаблон не регистрируется — иначе клиент видел бы ресурс,
// который никогда не отдаётся.
func TestResultResource_NotRegisteredWithoutSpool(t *testing.T) {
	sess := startInMem(t, newDeps(&fakeMB{}))
	lst, err := sess.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if len(lst.ResourceTemplates) != 0 {
		t.Errorf("шаблонов быть не должно: %+v", lst.ResourceTemplates)
	}
}

func TestWantFile(t *testing.T) {
	l := Limits{InlineMaxBytes: 100}
	cases := []struct {
		delivery string
		bytes    int
		limits   Limits
		want     bool
	}{
		{deliveryAuto, 50, l, false},
		{deliveryAuto, 100, l, false}, // ровно на пороге — ещё inline
		{deliveryAuto, 101, l, true},
		{deliveryInline, 1 << 20, l, false},
		{deliveryFile, 0, l, true},
		{deliveryAuto, 1 << 20, Limits{}, false}, // порог 0 → фича выключена
	}
	for _, tc := range cases {
		if got := wantFile(tc.delivery, tc.bytes, tc.limits); got != tc.want {
			t.Errorf("wantFile(%q, %d, %+v) = %v, want %v",
				tc.delivery, tc.bytes, tc.limits, got, tc.want)
		}
	}
}
