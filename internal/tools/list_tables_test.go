package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dezer32/metabase-mcp/internal/metabase"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ptr helper для FK-полей.
func intPtr(v int) *int { return &v }

func TestListTables_OK(t *testing.T) {
	mb := &fakeMB{
		meta: &metabase.MetadataRaw{
			Tables: []metabase.RawTable{
				{
					ID:     1,
					Name:   "users",
					Schema: "public",
					Fields: []metabase.RawField{
						{ID: 10, Name: "id", BaseType: "type/Integer", DatabaseRequired: true},
						{ID: 11, Name: "email", BaseType: "type/Text"},
					},
				},
				{
					ID:     2,
					Name:   "tickets",
					Schema: "public",
					Fields: []metabase.RawField{
						{ID: 20, Name: "id", BaseType: "type/Integer"},
						{ID: 21, Name: "user_id", BaseType: "type/Integer", FKTargetFieldID: intPtr(10)},
					},
				},
			},
		},
	}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_tables",
		Arguments: map[string]any{"database_id": 3},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %+v", res.Content)
	}

	raw, _ := json.Marshal(res.StructuredContent)
	var out listTablesOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (raw=%s)", err, raw)
	}

	if len(out.Tables) != 2 {
		t.Fatalf("tables: %d", len(out.Tables))
	}

	// Найдём tickets — там FK.
	var tickets *struct {
		ForeignKeys []struct {
			FromColumn string `json:"from_column"`
			ToTable    string `json:"to_table"`
			ToColumn   string `json:"to_column"`
		} `json:"foreign_keys"`
	}
	_ = tickets // удалим — проверим напрямую через out.Tables.

	var found bool
	for _, tbl := range out.Tables {
		if tbl.Name != "tickets" {
			continue
		}
		found = true
		if len(tbl.ForeignKeys) != 1 {
			t.Errorf("tickets FK count: %d", len(tbl.ForeignKeys))
			break
		}
		fk := tbl.ForeignKeys[0]
		if fk.FromColumn != "user_id" || fk.ToTable != "users" || fk.ToColumn != "id" {
			t.Errorf("fk: %+v", fk)
		}
	}
	if !found {
		t.Fatal("tickets not found")
	}
}

func TestListTables_BadID(t *testing.T) {
	mb := &fakeMB{}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_tables",
		Arguments: map[string]any{"database_id": 0},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for database_id=0")
	}
	if mb.metaCalls != 0 {
		t.Errorf("metadata should not have been fetched, but got %d calls", mb.metaCalls)
	}
}

func TestListTables_Cached(t *testing.T) {
	mb := &fakeMB{meta: &metabase.MetadataRaw{}}
	sess := startInMem(t, newDeps(mb))

	for i := 0; i < 3; i++ {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "list_tables",
			Arguments: map[string]any{"database_id": 7},
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if res.IsError {
			t.Fatalf("call %d IsError", i)
		}
	}
	if mb.metaCalls != 1 {
		t.Errorf("expected 1 backing call due to caching, got %d", mb.metaCalls)
	}
}

func TestListTables_Error(t *testing.T) {
	mb := &fakeMB{metaErr: errors.New("nope")}
	sess := startInMem(t, newDeps(mb))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_tables",
		Arguments: map[string]any{"database_id": 3},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError")
	}
}
