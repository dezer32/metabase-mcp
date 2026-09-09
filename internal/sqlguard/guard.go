// Package sqlguard валидирует пользовательский SQL ДО отправки в Metabase.
// Разрешает только read-only запросы: SELECT и WITH ... SELECT.
//
// Парсим в AST через TiDB parser. SQL-comment-обходчики типа
// "/* SELECT */ DROP TABLE x" не пройдут — парсер видит DROP.
package sqlguard

import (
	"errors"
	"fmt"
	"sync"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver" // регистрирует ValueExpr
)

// parserPool переиспользует *parser.Parser между запросами. Каждый New()
// аллоцирует AST-скаффолдинг, заметный на горячем пути execute_sql.
var parserPool = sync.Pool{
	New: func() any { return parser.New() },
}

// Validate возвращает nil, если query — это один валидный read-only SELECT/WITH.
// Иначе — ошибку с понятным текстом для пользователя.
//
// Вся работа — в Inspect: он и валидирует, и собирает факты о LIMIT/OFFSET
// за тот же один парс. Validate оставлен для вызывающих, которым факты
// не нужны.
func Validate(query string) error {
	_, err := Inspect(query)
	return err
}

// checkReadOnly проходит по AST и отказывает всему, что не read-only SELECT.
// SELECT INTO OUTFILE/DUMPFILE/@var, FOR UPDATE и LOCK IN SHARE MODE
// синтаксически разворачиваются в *ast.SelectStmt, но это уже не read-only.
func checkReadOnly(node ast.Node) error {
	switch s := node.(type) {
	case *ast.SelectStmt:
		if s.SelectIntoOpt != nil {
			return errors.New("SELECT INTO OUTFILE/DUMPFILE запрещён")
		}
		if s.LockInfo != nil && s.LockInfo.LockType != ast.SelectLockNone {
			return errors.New("FOR UPDATE / LOCK IN SHARE MODE запрещён")
		}
		return nil
	case *ast.SetOprStmt:
		// UNION/INTERSECT/EXCEPT: каждая ветвь — самостоятельный SELECT.
		if s.SelectList == nil || len(s.SelectList.Selects) == 0 {
			return errors.New("разрешены только SELECT/WITH")
		}
		for _, sel := range s.SelectList.Selects {
			if err := checkReadOnly(sel); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("разрешены только SELECT/WITH, получен %T", node)
	}
}
