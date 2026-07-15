package oauth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Register выполняет Dynamic Client Registration (RFC 7591) как публичный
// клиент: token_endpoint_auth_method=none, PKCE вместо секрета.
// Пропускается вызывающим кодом, если задан METABASE_OAUTH_CLIENT_ID.
//
// redirectURI обязан быть уже привязанным (см. BindCallback): его же мы
// регистрируем и его же используем в authorize/exchange — они должны совпадать.
func Register(ctx context.Context, meta *Meta, redirectURI string, scopes []string, hc *http.Client) (*oauthex.ClientRegistrationResponse, error) {
	if meta.RegisterEP == "" {
		return nil, fmt.Errorf("oauth: authorization server has no registration_endpoint; " +
			"set METABASE_OAUTH_CLIENT_ID to use a pre-registered client")
	}
	clientMeta := &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{redirectURI},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		Scope:                   strings.Join(scopes, " "),
		ClientName:              "metabase-mcp",
	}
	resp, err := oauthex.RegisterClient(ctx, meta.RegisterEP, clientMeta, hc)
	if err != nil {
		return nil, fmt.Errorf("oauth: dynamic client registration: %w", err)
	}
	if resp.ClientID == "" {
		return nil, fmt.Errorf("oauth: DCR response missing client_id")
	}
	return resp, nil
}
