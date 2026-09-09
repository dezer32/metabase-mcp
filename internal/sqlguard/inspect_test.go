package sqlguard

import "testing"

func TestInspect_Limit(t *testing.T) {
	cases := []struct {
		name      string
		q         string
		hasLimit  bool
		limit     int64
		hasOffset bool
		offset    int64
	}{
		{"no limit", "SELECT 1", false, 0, false, 0},
		{"limit", "SELECT 1 LIMIT 10", true, 10, false, 0},
		{"limit offset", "SELECT 1 LIMIT 10 OFFSET 5", true, 10, true, 5},
		// MySQL-форма "LIMIT offset, count": первое число — это OFFSET.
		{"limit comma", "SELECT 1 LIMIT 5, 10", true, 10, true, 5},
		{"CTE limit", "WITH x AS (SELECT 1 AS n) SELECT n FROM x LIMIT 10", true, 10, false, 0},
		// Парсер поднимает хвостовой лимит UNION'а на SetOprStmt.
		{"union limit", "SELECT 1 UNION SELECT 2 LIMIT 5", true, 5, false, 0},
		{"fetch first", "SELECT * FROM t FETCH FIRST 7 ROWS ONLY", true, 7, false, 0},
		// LIMIT ? — литерала нет, значение неизвестно.
		{"param marker", "SELECT 1 LIMIT ?", true, -1, false, 0},
		// Лимит в подзапросе итоговый набор не ограничивает.
		{"subquery limit", "SELECT * FROM (SELECT 1 AS n LIMIT 3) z", false, 0, false, 0},
		{"limit zero", "SELECT 1 LIMIT 0", true, 0, false, 0},
		{"trailing semicolon", "SELECT 1 LIMIT 10;", true, 10, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Inspect(tc.q)
			if err != nil {
				t.Fatalf("Inspect(%q) = %v, want nil", tc.q, err)
			}
			if info.HasLimit != tc.hasLimit {
				t.Errorf("HasLimit = %v, want %v", info.HasLimit, tc.hasLimit)
			}
			if info.HasLimit && info.Limit != tc.limit {
				t.Errorf("Limit = %d, want %d", info.Limit, tc.limit)
			}
			if info.HasOffset != tc.hasOffset {
				t.Errorf("HasOffset = %v, want %v", info.HasOffset, tc.hasOffset)
			}
			if info.HasOffset && info.Offset != tc.offset {
				t.Errorf("Offset = %d, want %d", info.Offset, tc.offset)
			}
		})
	}
}

// TestInspect_ParenthesizedUnionRejected фиксирует предсуществующее
// ограничение guard'а: скобочные ветви UNION парсер отдаёт как
// *ast.SetOprSelectList, а checkReadOnly знает только SelectStmt/SetOprStmt.
// Такой запрос отваливается ещё до анализа LIMIT'а, поэтому его «лимит
// остался в ветви» на автоподстановку не влияет.
func TestInspect_ParenthesizedUnionRejected(t *testing.T) {
	const q = "(SELECT 1) UNION (SELECT 2 LIMIT 5)"
	if _, err := Inspect(q); err == nil {
		t.Errorf("Inspect(%q) = nil; ожидался отказ checkReadOnly", q)
	}
}

// TestInspect_MatchesValidate — защита от регрессии рефакторинга: Inspect
// и Validate обязаны соглашаться друг с другом на всех кейсах guard_test.
func TestInspect_MatchesValidate(t *testing.T) {
	var queries []string
	for _, tc := range validatePositiveCases() {
		queries = append(queries, tc.q)
	}
	for _, tc := range validateNegativeCases() {
		queries = append(queries, tc.q)
	}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			_, ierr := Inspect(q)
			verr := Validate(q)
			if (ierr != nil) != (verr != nil) {
				t.Errorf("Inspect(%q) = %v, Validate = %v: должны соглашаться", q, ierr, verr)
			}
		})
	}
}
