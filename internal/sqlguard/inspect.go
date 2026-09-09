package sqlguard

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
)

// Info — факты о верхнеуровневом запросе, собранные за один парс.
//
// «Верхнеуровневый» здесь буквально: LIMIT в подзапросе или внутри CTE
// не ограничивает итоговый набор, поэтому в Info он не попадает.
type Info struct {
	HasLimit  bool  // LIMIT/FETCH FIRST на самом верхнем уровне
	HasOffset bool  // OFFSET, либо первая часть формы LIMIT o, c
	Limit     int64 // литеральное значение; -1 если это LIMIT ?
	Offset    int64 // литеральное значение; -1 если это OFFSET ?
	CodeEnd   int   // offset за последним значащим байтом SQL
}

// Inspect валидирует query как единственный read-only SELECT/WITH и заодно
// собирает факты о верхнеуровневом LIMIT/OFFSET. Один парс — два ответа:
// Validate — тонкая обёртка над Inspect, второй парс на горячем пути
// execute_sql не нужен.
func Inspect(query string) (Info, error) {
	if strings.TrimSpace(query) == "" {
		return Info{}, errors.New("empty query")
	}

	p, ok := parserPool.Get().(*parser.Parser)
	if !ok {
		p = parser.New()
	}
	defer parserPool.Put(p)

	stmts, _, err := p.Parse(query, "", "")
	if err != nil {
		return Info{}, fmt.Errorf("syntax error: %w", err)
	}
	if len(stmts) == 0 {
		// Голый комментарий или пробел.
		return Info{}, errors.New("empty query")
	}
	if len(stmts) != 1 {
		return Info{}, errors.New("разрешён ровно один statement")
	}
	if err := checkReadOnly(stmts[0]); err != nil {
		return Info{}, err
	}

	info := Info{CodeEnd: codeEnd(query)}
	if lim := topLimit(stmts[0]); lim != nil && lim.Count != nil {
		info.HasLimit = true
		info.Limit = literalInt64(lim.Count)
		if lim.Offset != nil {
			info.HasOffset = true
			info.Offset = literalInt64(lim.Offset)
		}
	}
	return info, nil
}

// topLimit достаёт LIMIT самого верхнего уровня.
//
// Для UNION/INTERSECT/EXCEPT парсер ПОДНИМАЕТ хвостовой лимит на
// SetOprStmt ("SELECT 1 UNION SELECT 2 LIMIT 5" → SetOprStmt.Limit),
// а лимит внутри скобочной ветви так и остаётся в ветви
// ("(SELECT 1) UNION (SELECT 2 LIMIT 5)" → top-level лимита нет).
// Это верно: такой лимит итоговый набор не ограничивает.
func topLimit(node ast.Node) *ast.Limit {
	switch s := node.(type) {
	case *ast.SelectStmt:
		return s.Limit
	case *ast.SetOprStmt:
		return s.Limit
	default:
		return nil
	}
}

// literalInt64 достаёт числовое значение литерала LIMIT/OFFSET.
// Парсер строит его через ast.NewValueExpr(uint64), но принимаем и
// int64/int — на случай смены представления в апстриме.
// LIMIT ? (ParamMarkerExpr) отдаёт GetValue() == nil, для него возвращаем -1.
func literalInt64(e ast.ExprNode) int64 {
	v, ok := e.(ast.ValueExpr)
	if !ok {
		return -1
	}
	switch n := v.GetValue().(type) {
	case uint64:
		if n > math.MaxInt64 {
			return -1
		}
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return -1
	}
}
