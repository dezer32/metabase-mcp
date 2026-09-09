package spool

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// URITemplate — RFC 6570 шаблон, под которым результаты регистрируются
// как MCP-ресурс. {id} по умолчанию не матчит '/', а id у нас hex —
// поэтому шаблон и формат id согласованы.
const URITemplate = "metabase://result/{id}"

// uriPrefix — то же самое без {id}.
const uriPrefix = "metabase://result/"

// ErrBadURI — URI не похож на metabase://result/<id>.
var ErrBadURI = errors.New("spool: malformed result URI")

// idPattern — ровно 16 hex-символов (8 байт из crypto/rand).
// Строгий матч здесь же выполняет роль защиты от path traversal:
// "metabase://result/../../etc/passwd" не пройдёт.
var idPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// URIFor собирает URI ресурса по id.
func URIFor(id string) string { return uriPrefix + id }

// ParseURI принимает ровно metabase://result/<16 hex> и возвращает id.
func ParseURI(uri string) (string, error) {
	id, ok := strings.CutPrefix(uri, uriPrefix)
	if !ok || !idPattern.MatchString(id) {
		return "", fmt.Errorf("%w: %q", ErrBadURI, uri)
	}
	return id, nil
}
