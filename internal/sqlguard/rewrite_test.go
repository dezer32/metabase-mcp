package sqlguard

import (
	"strings"
	"testing"
)

func TestWithLimitOffset_Insertion(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want string
	}{
		{"plain", "SELECT 1", "SELECT 1 LIMIT 1000 OFFSET 0"},
		{"semicolon", "SELECT 1;", "SELECT 1 LIMIT 1000 OFFSET 0;"},
		{"semicolon spaces", "SELECT 1  ;  ", "SELECT 1 LIMIT 1000 OFFSET 0  ;  "},
		// Наивный append сломался бы: клауза уехала бы за ';' И внутрь --.
		{"semicolon then comment", "SELECT 1; -- note", "SELECT 1 LIMIT 1000 OFFSET 0; -- note"},
		{"trailing line comment", "SELECT 1 -- note", "SELECT 1 LIMIT 1000 OFFSET 0 -- note"},
		{"trailing hash comment", "SELECT 1 # note", "SELECT 1 LIMIT 1000 OFFSET 0 # note"},
		{"trailing block comment", "SELECT 1 /* note */", "SELECT 1 LIMIT 1000 OFFSET 0 /* note */"},
		// ';' внутри литерала — значащий байт, а не терминатор.
		{"semicolon in string", "SELECT ';' AS s", "SELECT ';' AS s LIMIT 1000 OFFSET 0"},
		{"dashes in string", "SELECT '-- x' AS s", "SELECT '-- x' AS s LIMIT 1000 OFFSET 0"},
		{"hash in string", "SELECT '#x' AS s", "SELECT '#x' AS s LIMIT 1000 OFFSET 0"},
		{"block open in string", "SELECT '/* x' AS s", "SELECT '/* x' AS s LIMIT 1000 OFFSET 0"},
		// "--" без пробела — это минус-минус, а не комментарий (lexer.go).
		{"minus minus", "SELECT 1--2", "SELECT 1--2 LIMIT 1000 OFFSET 0"},
		{"backquoted ident", "SELECT `a;b` FROM t", "SELECT `a;b` FROM t LIMIT 1000 OFFSET 0"},
		{"newline comment then code", "SELECT 1 -- note\n+ 2", "SELECT 1 -- note\n+ 2 LIMIT 1000 OFFSET 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Inspect(tc.q)
			if err != nil {
				t.Fatalf("Inspect(%q) = %v", tc.q, err)
			}
			if got := WithLimitOffset(tc.q, info, 1000, 0); got != tc.want {
				t.Errorf("WithLimitOffset(%q) =\n  %q\nwant\n  %q", tc.q, got, tc.want)
			}
		})
	}
}

// TestWithLimitOffset_RoundTrip — самый ценный тест части A: свой лексер
// хвоста проверяется настоящим парсером. Для каждого позитивного кейса
// guard_test дописанный SQL обязан снова пройти Validate и получить
// верхнеуровневый LIMIT с ровно теми значениями, что мы вставили.
func TestWithLimitOffset_RoundTrip(t *testing.T) {
	cases := validatePositiveCases()
	// Хвосты, на которых ломается наивный append.
	cases = append(cases,
		validateCase{"semicolon then comment", "SELECT 1; -- note", ""},
		validateCase{"trailing line comment", "SELECT 1 -- note", ""},
		validateCase{"trailing hash comment", "SELECT 1 # note", ""},
		validateCase{"trailing block comment", "SELECT 1 /* note */", ""},
		validateCase{"semicolon inside string", "SELECT ';' AS s", ""},
		validateCase{"minus minus", "SELECT 1--2", ""},
		validateCase{"backquoted semicolon", "SELECT `a;b` FROM t", ""},
		validateCase{"escaped quote", `SELECT '\'' AS s`, ""},
		validateCase{"doubled quote", `SELECT '''' AS s`, ""},
		validateCase{"multiline", "SELECT id\nFROM users\nWHERE id > 0\n", ""},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Inspect(tc.q)
			if err != nil {
				t.Fatalf("Inspect(%q) = %v", tc.q, err)
			}
			if info.HasLimit {
				t.Skipf("кейс уже содержит top-level LIMIT")
			}
			got := WithLimitOffset(tc.q, info, 1000, 250)

			if err := Validate(got); err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", got, err)
			}
			gotInfo, err := Inspect(got)
			if err != nil {
				t.Fatalf("Inspect(%q) = %v", got, err)
			}
			if !gotInfo.HasLimit {
				t.Errorf("после вставки HasLimit=false: %q", got)
			}
			if gotInfo.Limit != 1000 {
				t.Errorf("Limit = %d, want 1000: %q", gotInfo.Limit, got)
			}
			if !gotInfo.HasOffset || gotInfo.Offset != 250 {
				t.Errorf("Offset = (%v, %d), want (true, 250): %q",
					gotInfo.HasOffset, gotInfo.Offset, got)
			}
			// Исходный текст обязан сохраниться целиком: мы только вставляем.
			if !strings.HasPrefix(got, tc.q[:info.CodeEnd]) {
				t.Errorf("голова запроса испорчена: %q", got)
			}
			if !strings.HasSuffix(got, tc.q[info.CodeEnd:]) {
				t.Errorf("хвост запроса испорчен: %q", got)
			}
		})
	}
}

func TestCodeEnd(t *testing.T) {
	cases := []struct {
		q    string
		want int
	}{
		{"SELECT 1", 8},
		{"SELECT 1;", 8},
		{"SELECT 1  ;  ", 8},
		{"SELECT 1; -- note", 8},
		{"SELECT 1 /* note */", 8},
		{"", 0},
		{"   ", 0},
		{"/* only comment */", 0},
		{"-- only comment", 0},
		{"SELECT ';'", 10},
		{"SELECT 1--2", 11},
	}
	for _, tc := range cases {
		if got := codeEnd(tc.q); got != tc.want {
			t.Errorf("codeEnd(%q) = %d, want %d", tc.q, got, tc.want)
		}
	}
}
