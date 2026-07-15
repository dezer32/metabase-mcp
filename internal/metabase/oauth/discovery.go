// Package oauth реализует OAuth 2.1 (Authorization Code + PKCE + refresh)
// авторизацию metabase-mcp как OAuth-клиента к Metabase.
//
// Поток: Discover (RFC 9728 PRM + RFC 8414 AS metadata) → опциональный DCR
// (RFC 7591) → интерактивный вход в браузере (RFC 6749 + PKCE RFC 7636) →
// персист токена → тихий refresh (с ротацией refresh-токена).
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/dezer32/metabase-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Meta — сведённые метаданные защищённого ресурса и его AS,
// нужные для DCR, интерактивного входа и refresh.
type Meta struct {
	Issuer                   string   // authorization server issuer
	AuthorizeEP              string   // authorization_endpoint
	TokenEP                  string   // token_endpoint
	RegisterEP               string   // registration_endpoint (может быть пустым)
	Resource                 string   // RFC 8707 resource (из PRM)
	ScopesSupported          []string // PRM scopes_supported
	TokenEndpointAuthMethods []string // AS token_endpoint_auth_methods_supported
	GrantTypesSupported      []string // AS grant_types_supported
}

// Discover тянет метаданные защищённого ресурса и его authorization server.
//
// PRM запрашивается ПРЯМЫМ net/http GET (а не oauthex.GetProtectedResourceMetadata),
// потому что SDK-геттер строго требует заранее знать resource и сверяет его.
// Мы, наоборот, достаём resource из ответа и делаем его авторитетным.
func Discover(ctx context.Context, cfg config.Config, hc *http.Client) (*Meta, error) {
	prm, err := fetchPRM(ctx, cfg.MetabaseURL, hc)
	if err != nil {
		return nil, err
	}
	if prm.Resource == "" {
		return nil, fmt.Errorf("oauth: protected resource metadata has empty resource")
	}
	// Cross-check: resource, который мы будем слать как RFC 8707 параметр,
	// обязан совпадать с тем, что объявляет сервер. Дефолт из config совпадает
	// с реальным сервером; несовпадение — это либо кривой override, либо смена
	// resource на сервере — в обоих случаях лучше упасть явно.
	if cfg.OAuthResource != "" && cfg.OAuthResource != prm.Resource {
		return nil, fmt.Errorf("oauth: METABASE_OAUTH_RESOURCE %q does not match protected resource %q "+
			"(set it to match or unset to auto-derive)", cfg.OAuthResource, prm.Resource)
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("oauth: protected resource metadata lists no authorization_servers")
	}
	issuer := prm.AuthorizationServers[0]

	// (nil, nil) означает «все well-known вернули 404» — трактуем как ошибку.
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, hc)
	if err != nil {
		return nil, fmt.Errorf("oauth: fetch authorization server metadata: %w", err)
	}
	if asm == nil {
		return nil, fmt.Errorf("oauth: no authorization server metadata for issuer %q "+
			"(all well-known endpoints returned 404)", issuer)
	}
	if asm.TokenEndpoint == "" {
		return nil, fmt.Errorf("oauth: authorization server metadata missing token_endpoint")
	}
	if asm.AuthorizationEndpoint == "" {
		return nil, fmt.Errorf("oauth: authorization server metadata missing authorization_endpoint")
	}
	// PKCE S256 обязателен для публичного клиента.
	if !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return nil, fmt.Errorf("oauth: authorization server does not advertise S256 PKCE "+
			"(code_challenge_methods_supported=%v)", asm.CodeChallengeMethodsSupported)
	}

	return &Meta{
		Issuer:                   issuer,
		AuthorizeEP:              asm.AuthorizationEndpoint,
		TokenEP:                  asm.TokenEndpoint,
		RegisterEP:               asm.RegistrationEndpoint,
		Resource:                 prm.Resource,
		ScopesSupported:          prm.ScopesSupported,
		TokenEndpointAuthMethods: asm.TokenEndpointAuthMethodsSupported,
		GrantTypesSupported:      asm.GrantTypesSupported,
	}, nil
}

// fetchPRM делает прямой GET .../.well-known/oauth-protected-resource
// и парсит JSON в структуру SDK (переиспользуем тип, но не его геттер).
func fetchPRM(ctx context.Context, metabaseURL string, hc *http.Client) (*oauthex.ProtectedResourceMetadata, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	u := metabaseURL + "/.well-known/oauth-protected-resource"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("oauth: build PRM request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: fetch PRM: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: read PRM: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth: PRM endpoint %s returned status %d", u, resp.StatusCode)
	}
	var prm oauthex.ProtectedResourceMetadata
	if err := json.Unmarshal(body, &prm); err != nil {
		return nil, fmt.Errorf("oauth: decode PRM: %w", err)
	}
	return &prm, nil
}
