package schema

import (
	"reflect"
	"testing"

	"github.com/dezer32/metabase-mcp/internal/metabase"
)

func ptr[T any](v T) *T { return &v }

func TestFlatten_Basic(t *testing.T) {
	// Таблица users(id, name), таблица orders(id, user_id→users.id).
	raw := &metabase.MetadataRaw{
		Tables: []metabase.RawTable{
			{
				ID:     1,
				Name:   "users",
				Schema: "public",
				Fields: []metabase.RawField{
					{ID: 10, Name: "id", BaseType: "type/Integer", DatabaseType: "INT"},
					{ID: 11, Name: "name", BaseType: "type/Text", DatabaseType: "VARCHAR"},
				},
			},
			{
				ID:     2,
				Name:   "orders",
				Schema: "public",
				Fields: []metabase.RawField{
					{ID: 20, Name: "id", BaseType: "type/Integer", DatabaseType: "INT"},
					{
						ID:              21,
						Name:            "user_id",
						BaseType:        "type/Integer",
						DatabaseType:    "INT",
						FKTargetFieldID: ptr(10),
					},
				},
			},
		},
	}

	tables := Flatten(raw)

	if len(tables) != 2 {
		t.Fatalf("want 2 tables, got %d", len(tables))
	}

	// orders должен содержать FK на users.id.
	var orders *Table
	for i := range tables {
		if tables[i].Name == "orders" {
			orders = &tables[i]
		}
	}
	if orders == nil {
		t.Fatal("orders not found")
	}
	if len(orders.ForeignKeys) != 1 {
		t.Fatalf("orders fk count: %d", len(orders.ForeignKeys))
	}
	fk := orders.ForeignKeys[0]
	want := ForeignKey{FromColumn: "user_id", ToTable: "users", ToColumn: "id"}
	if !reflect.DeepEqual(fk, want) {
		t.Errorf("fk: got %+v want %+v", fk, want)
	}
}

func TestFlatten_NullableInferredFromRequired(t *testing.T) {
	raw := &metabase.MetadataRaw{
		Tables: []metabase.RawTable{
			{
				Name: "t",
				Fields: []metabase.RawField{
					{Name: "required_col", BaseType: "type/Text", DatabaseRequired: true},
					{Name: "nullable_col", BaseType: "type/Text", DatabaseRequired: false},
				},
			},
		},
	}
	tables := Flatten(raw)
	if len(tables) != 1 {
		t.Fatalf("tables: %d", len(tables))
	}
	cols := tables[0].Columns
	if cols[0].Nullable {
		t.Errorf("required_col should not be nullable")
	}
	if !cols[1].Nullable {
		t.Errorf("nullable_col should be nullable")
	}
}

func TestFlatten_FKToUnknownIsDropped(t *testing.T) {
	raw := &metabase.MetadataRaw{
		Tables: []metabase.RawTable{
			{
				Name: "orphan",
				Fields: []metabase.RawField{
					{Name: "phantom_id", FKTargetFieldID: ptr(99999)},
				},
			},
		},
	}
	tables := Flatten(raw)
	if len(tables[0].ForeignKeys) != 0 {
		t.Errorf("unresolvable FK should be dropped, got %+v", tables[0].ForeignKeys)
	}
}

func TestFlatten_NoFK(t *testing.T) {
	raw := &metabase.MetadataRaw{
		Tables: []metabase.RawTable{
			{
				Name: "simple",
				Fields: []metabase.RawField{
					{Name: "id"},
				},
			},
		},
	}
	tables := Flatten(raw)
	if len(tables[0].ForeignKeys) != 0 {
		t.Errorf("expected no FK, got %+v", tables[0].ForeignKeys)
	}
}

func TestFlatten_TypeNormalization(t *testing.T) {
	raw := &metabase.MetadataRaw{
		Tables: []metabase.RawTable{
			{
				Name: "t",
				Fields: []metabase.RawField{
					{Name: "a", BaseType: "type/Integer"},
					{Name: "b", BaseType: "type/Text"},
					{Name: "c", BaseType: "type/DateTimeWithTZ"},
					{Name: "d", BaseType: "type/Boolean"},
					{Name: "e", BaseType: ""}, // пусто — оставляем пусто
				},
			},
		},
	}
	tables := Flatten(raw)
	cols := tables[0].Columns
	expect := []string{"integer", "text", "datetimewithtz", "boolean", ""}
	for i, c := range cols {
		if c.Type != expect[i] {
			t.Errorf("col[%d].Type = %q, want %q", i, c.Type, expect[i])
		}
	}
}
