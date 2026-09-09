package sqlguard

import (
	"strconv"
	"unicode"
)

// WithLimitOffset вставляет "LIMIT <limit> OFFSET <offset>" в позицию
// info.CodeEnd, сохраняя хвост запроса (';', комментарии) нетронутым:
//
//	"SELECT 1"              → "SELECT 1 LIMIT 1000 OFFSET 0"
//	"SELECT 1;"             → "SELECT 1 LIMIT 1000 OFFSET 0;"
//	"SELECT 1; -- заметка"  → "SELECT 1 LIMIT 1000 OFFSET 0; -- заметка"
//	"SELECT ';' AS s"       → "SELECT ';' AS s LIMIT 1000 OFFSET 0"
//
// Именно вставка, а не append в конец строки: TrimSuffix(q, ";") ломается на
// "SELECT 1; -- заметка" (TiDB считает это одним statement, и клауза уедет
// за ';'), а append без учёта хвостового --/#//* */ просто проглатывается
// комментарием.
//
// Вызывать имеет смысл только при !info.HasLimit — иначе получится два
// LIMIT'а. Проверка на стороне вызывающего.
func WithLimitOffset(q string, info Info, limit, offset int64) string {
	at := info.CodeEnd
	if at < 0 || at > len(q) {
		at = len(q)
	}
	return q[:at] + " LIMIT " + strconv.FormatInt(limit, 10) +
		" OFFSET " + strconv.FormatInt(offset, 10) + q[at:]
}

// codeEnd возвращает offset за последним значащим байтом SQL: завершающие
// пробелы, ';' и комментарии в него не входят.
//
// Это второй, независимый от TiDB, лексер — parser.Scanner.Lex наружу
// непригоден (принимает неэкспортированный yySymType), а OriginalText()
// включает ';'. Состояния повторяют lexer.go: '…' и "…" (с удвоением
// кавычки и \-escape), `…` (без \-escape), --/# до конца строки, /*…*/.
// Держит его честным round-trip тест TestWithLimitOffset_RoundTrip.
func codeEnd(q string) int {
	end := 0
	for i := 0; i < len(q); {
		switch c := q[i]; {
		case c == '\'' || c == '"':
			i = skipQuoted(q, i, c, true)
			end = i
		case c == '`':
			i = skipQuoted(q, i, c, false)
			end = i
		case c == '#':
			i = skipLineComment(q, i)
		case c == '-' && isLineCommentDash(q, i):
			i = skipLineComment(q, i)
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			i = skipBlockComment(q, i)
		case c == ';' || isSpaceByte(c):
			i++
		default:
			i++
			end = i
		}
	}
	return end
}

// isLineCommentDash: "--" открывает комментарий только если за ним конец
// ввода или пробельный символ (lexer.go, startWithDash). "1--2" — это
// минус-минус, а не комментарий.
func isLineCommentDash(q string, i int) bool {
	if i+1 >= len(q) || q[i+1] != '-' {
		return false
	}
	return i+2 >= len(q) || unicode.IsSpace(rune(q[i+2]))
}

// skipLineComment пропускает --/# комментарий вместе с завершающим \n.
func skipLineComment(q string, i int) int {
	for ; i < len(q); i++ {
		if q[i] == '\n' {
			return i + 1
		}
	}
	return i
}

// skipBlockComment пропускает /*…*/, включая /*!…*/ и /*+…*/.
// Незакрытый комментарий съедает остаток строки — но такой SQL всё равно
// не пройдёт Inspect, поэтому до WithLimitOffset дело не дойдёт.
func skipBlockComment(q string, i int) int {
	for j := i + 2; j+1 < len(q); j++ {
		if q[j] == '*' && q[j+1] == '/' {
			return j + 2
		}
	}
	return len(q)
}

// skipQuoted пропускает литерал, открытый кавычкой quote в позиции i,
// и возвращает offset за закрывающей кавычкой. Удвоенная кавычка
// литерал не закрывает; backslash экранирует следующий байт в '…' и "…",
// но не в `…` — так же, как в MySQL.
func skipQuoted(q string, i int, quote byte, backslash bool) int {
	for j := i + 1; j < len(q); j++ {
		switch {
		case backslash && q[j] == '\\':
			j++ // экранированный байт пропускаем целиком
		case q[j] == quote:
			if j+1 < len(q) && q[j+1] == quote {
				j++ // удвоение — часть литерала
				continue
			}
			return j + 1
		}
	}
	return len(q)
}

// isSpaceByte — ASCII-пробельные, как их видит лексер.
func isSpaceByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}
