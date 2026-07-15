package metabase

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// authProvider абстрагирует способ аутентификации запроса к Metabase.
//
// Наружу отдаётся НЕ секрет, а непрозрачный credential — монотонный
// generation. Он нужен только для условной инвалидизации по 401:
// invalidate(gen) обновит credential ТОЛЬКО если gen всё ещё актуален
// (иначе параллельная горутина уже обновила — и «протухший 401» не должен
// сбрасывать свежий токен/сессию).
type authProvider interface {
	// apply атомарно снимает текущий credential и его generation одним
	// снимком (под одним мьютексом), ставит соответствующий заголовок и
	// возвращает именно этот generation.
	apply(ctx context.Context, req *http.Request) (cred uint64, err error)
	// invalidate условно инвалидирует credential с данным generation.
	// Для oauth это сетевой force-refresh + Save, поэтому ctx и error.
	invalidate(ctx context.Context, cred uint64) error
}

// classify401 разбирает WWW-Authenticate у 401-ответа и решает, имеет ли смысл
// обновлять credential и повторять запрос.
//
//   - нет заголовка / не-Bearer / error=invalid_token / голый Bearer →
//     retryable (истёкший токен или классическая сессия Metabase);
//   - insufficient_scope / audience / прочий error → НЕ retryable: рефреш это
//     не починит, нужно вернуть диагностику.
func classify401(header http.Header) (retryable bool, diag string) {
	vals := header.Values("WWW-Authenticate")
	if len(vals) == 0 {
		return true, ""
	}
	challenges, err := oauthex.ParseWWWAuthenticate(vals)
	if err != nil {
		return true, ""
	}
	for _, ch := range challenges {
		if ch.Scheme != "bearer" {
			continue
		}
		switch code := ch.Params["error"]; code {
		case "", "invalid_token":
			return true, ""
		default:
			desc := ch.Params["error_description"]
			if desc != "" {
				return false, fmt.Sprintf("%s: %s", code, desc)
			}
			return false, code
		}
	}
	// Bearer-челленджа с ошибкой не нашли — считаем истёкшим токеном.
	return true, ""
}
