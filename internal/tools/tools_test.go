package tools

import (
	"context"
	"testing"
)

// TestTools_ReadOnlyAnnotations — страж read-only хинтов: каждый tool
// сервера обязан объявить полный набор из readOnlyTool, иначе хост увидит
// дефолты спецификации и примет его за опасный.
func TestTools_ReadOnlyAnnotations(t *testing.T) {
	sess := startInMem(t, newDeps(&fakeMB{}))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	seen := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		seen[tool.Name] = true
		a := tool.Annotations
		if a == nil {
			t.Errorf("%s: annotations отсутствуют", tool.Name)
			continue
		}
		if !a.ReadOnlyHint {
			t.Errorf("%s: readOnlyHint = false", tool.Name)
		}
		if !a.IdempotentHint {
			t.Errorf("%s: idempotentHint = false", tool.Name)
		}
		if a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s: destructiveHint должен быть явным false", tool.Name)
		}
		if a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: openWorldHint должен быть явным false", tool.Name)
		}
		if tool.Title == "" || a.Title != tool.Title {
			t.Errorf("%s: title = %q, annotations.title = %q", tool.Name, tool.Title, a.Title)
		}
	}
	for _, name := range []string{"list_databases", "list_tables", "execute_sql"} {
		if !seen[name] {
			t.Errorf("tool %s не зарегистрирован", name)
		}
	}
}
