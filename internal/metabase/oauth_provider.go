package metabase

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dezer32/metabase-mcp/internal/metabase/oauth"
)

// Проверка на этапе компиляции: oauthProvider реализует authProvider.
var _ authProvider = (*oauthProvider)(nil)

// oauthProvider — authProvider поверх oauth.Manager (Bearer-токен).
type oauthProvider struct {
	mgr *oauth.Manager
}

// apply ставит Authorization: Bearer <access> из атомарного снимка токена
// и возвращает его generation.
func (p *oauthProvider) apply(ctx context.Context, req *http.Request) (uint64, error) {
	tok, gen, err := p.mgr.Token(ctx)
	if err != nil {
		return 0, fmt.Errorf("oauth token: %w", err)
	}
	req.Header.Set("Authorization", tok.Type()+" "+tok.AccessToken)
	return gen, nil
}

// invalidate форс-рефрешит токен, если его generation ещё актуален.
// Ошибку рефреша пробрасывает наверх (не ретраить со старым токеном).
func (p *oauthProvider) invalidate(ctx context.Context, cred uint64) error {
	return p.mgr.ForceRefresh(ctx, cred)
}
