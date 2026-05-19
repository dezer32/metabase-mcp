package schema

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dezer32/metabase-mcp/internal/metabase"
)

func TestRows_Basic(t *testing.T) {
	cols := []metabase.DatasetCol{
		{Name: "id", BaseType: "type/Integer"},
		{Name: "name", BaseType: "type/Text"},
	}
	rows := [][]any{{1, "alice"}, {2, "bob"}}

	res := Rows(cols, rows, 5)
	if res.Meta.RowCount != 2 {
		t.Errorf("RowCount: %d", res.Meta.RowCount)
	}
	if res.Meta.RunningMs != 5 {
		t.Errorf("RunningMs: %d", res.Meta.RunningMs)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows: %d", len(res.Rows))
	}
	if res.Rows[0]["id"] != 1 {
		t.Errorf("row 0 id: %v", res.Rows[0]["id"])
	}
	if res.Rows[1]["name"] != "bob" {
		t.Errorf("row 1 name: %v", res.Rows[1]["name"])
	}
}

func TestRows_NullValues(t *testing.T) {
	cols := []metabase.DatasetCol{{Name: "x"}}
	rows := [][]any{{nil}}
	res := Rows(cols, rows, 0)
	if got, ok := res.Rows[0]["x"]; !ok {
		t.Errorf("nil should be present as nil key")
	} else if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestRows_DuplicateColumns(t *testing.T) {
	// SELECT a.id, b.id, b.id из двух таблиц → дубликаты «id».
	cols := []metabase.DatasetCol{
		{Name: "id", BaseType: "type/Integer"},
		{Name: "id", BaseType: "type/Integer"},
		{Name: "id", BaseType: "type/Integer"},
	}
	rows := [][]any{{1, 2, 3}}

	res := Rows(cols, rows, 0)
	if len(res.Rows[0]) != 3 {
		t.Fatalf("map should have 3 keys, got %d: %+v", len(res.Rows[0]), res.Rows[0])
	}
	if res.Rows[0]["id"] != 1 {
		t.Errorf("id (first): %v", res.Rows[0]["id"])
	}
	if res.Rows[0]["id_2"] != 2 {
		t.Errorf("id_2: %v", res.Rows[0]["id_2"])
	}
	if res.Rows[0]["id_3"] != 3 {
		t.Errorf("id_3: %v", res.Rows[0]["id_3"])
	}

	// Meta.Columns должен сохранить оригинальные имена.
	wantMeta := []ColumnMeta{
		{Name: "id", Key: "id", Type: "integer"},
		{Name: "id", Key: "id_2", Type: "integer"},
		{Name: "id", Key: "id_3", Type: "integer"},
	}
	if !reflect.DeepEqual(res.Meta.Columns, wantMeta) {
		t.Errorf("meta.columns mismatch:\n got %+v\nwant %+v", res.Meta.Columns, wantMeta)
	}
}

func TestRows_TimeStaysAsString(t *testing.T) {
	cols := []metabase.DatasetCol{{Name: "ts", BaseType: "type/DateTime"}}
	rows := [][]any{{"2026-01-15T10:30:00Z"}}
	res := Rows(cols, rows, 0)
	got := res.Rows[0]["ts"]
	if got != "2026-01-15T10:30:00Z" {
		t.Errorf("time should stay as string: %v (%T)", got, got)
	}
}

func TestRows_EncodedJSONShape(t *testing.T) {
	cols := []metabase.DatasetCol{
		{Name: "id", BaseType: "type/Integer"},
	}
	rows := [][]any{{1}, {2}}
	res := Rows(cols, rows, 7)

	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := back["rows"]; !ok {
		t.Errorf("top-level 'rows' missing: %s", encoded)
	}
	if _, ok := back["meta"]; !ok {
		t.Errorf("top-level 'meta' missing: %s", encoded)
	}
}

func TestUniquify(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{[]string{"a", "a", "a"}, []string{"a", "a_2", "a_3"}},
		{[]string{"x", "y", "x"}, []string{"x", "y", "x_2"}},
		{nil, []string{}},
	}
	for _, tc := range cases {
		got := uniquify(tc.in)
		// Сравниваем «как массивы», nil ↔ [] разрешено.
		if len(got) != len(tc.want) {
			t.Errorf("uniquify(%v) len mismatch: %v vs %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("uniquify(%v)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
