package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/dezer32/metabase-mcp/internal/config"
)

// fakeAS — управляемый fake OAuth 2.1 authorization server + protected resource
// для тестов oauth-пакета. Отдаёт RFC 9728 PRM, RFC 8414 AS metadata,
// /oauth/register (DCR), /oauth/authorize и /oauth/token. Поведение всех
// эндпоинтов настраивается полями под mu.
type fakeAS struct {
	srv *httptest.Server

	mu sync.Mutex

	// well-known
	resourceOverride     string   // если "" → srv.URL + "/api/metabase-mcp"
	scopesSupported      []string // PRM scopes_supported
	codeChallengeMethods []string // AS code_challenge_methods_supported
	tokenAuthMethods     []string // AS token_endpoint_auth_methods_supported
	grantTypes           []string
	hidePRM              bool // PRM well-known → 404
	hideAuthServerMeta   bool // AS well-known → 404

	// DCR
	registerCalls int
	issuedClient  string // client_id, выдаваемый DCR (дефолт "dcr-client")

	// authorize/token — заполняются тестами login/source.
	authorizeCalls    int
	tokenCalls        int
	lastAuthQuery     map[string]string // последний query authorize
	lastTokenForm     map[string]string // последний POST-форм token
	issuedRefresh     string            // текущий валидный refresh_token
	accessCounter     int               // инкремент на каждый выданный access
	rotateRefresh     bool              // если true — token endpoint ротирует refresh
	tokenErrorCode    string            // если задан — token endpoint отвечает 400 с этим error
	omitRefreshTok    bool              // если true — exchange не вернёт refresh_token
	expiresInSecond   int               // expires_in ответа (дефолт 3600)
	authStateOverride string            // если задан — authorize вернёт этот state вместо эха
	authErrorParam    string            // если задан — authorize редиректит с ?error=...
}

func newFakeAS() *fakeAS {
	f := &fakeAS{
		scopesSupported:      []string{"mb:full", "agent:read"},
		codeChallengeMethods: []string{"S256"},
		tokenAuthMethods:     []string{"none", "client_secret_post"},
		grantTypes:           []string{"authorization_code", "refresh_token"},
		issuedClient:         "dcr-client",
		issuedRefresh:        "refresh-0",
		expiresInSecond:      3600,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", f.handlePRM)
	mux.HandleFunc("/.well-known/oauth-authorization-server", f.handleASMeta)
	mux.HandleFunc("/oauth/register", f.handleRegister)
	mux.HandleFunc("/oauth/authorize", f.handleAuthorize)
	mux.HandleFunc("/oauth/token", f.handleToken)
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeAS) URL() string { return f.srv.URL }
func (f *fakeAS) Close()      { f.srv.Close() }

func (f *fakeAS) resource() string {
	if f.resourceOverride != "" {
		return f.resourceOverride
	}
	return f.srv.URL + "/api/metabase-mcp"
}

func (f *fakeAS) handlePRM(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hide := f.hidePRM
	scopes := f.scopesSupported
	f.mu.Unlock()
	if hide {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{
		"resource":                 f.resource(),
		"authorization_servers":    []string{f.srv.URL},
		"scopes_supported":         scopes,
		"bearer_methods_supported": []string{"header"},
	})
}

func (f *fakeAS) handleASMeta(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hide := f.hideAuthServerMeta
	ccm := f.codeChallengeMethods
	tam := f.tokenAuthMethods
	gt := f.grantTypes
	f.mu.Unlock()
	if hide {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{
		"issuer":                                f.srv.URL,
		"authorization_endpoint":                f.srv.URL + "/oauth/authorize",
		"token_endpoint":                        f.srv.URL + "/oauth/token",
		"registration_endpoint":                 f.srv.URL + "/oauth/register",
		"scopes_supported":                      f.scopesSupported,
		"grant_types_supported":                 gt,
		"code_challenge_methods_supported":      ccm,
		"token_endpoint_auth_methods_supported": tam,
		"response_types_supported":              []string{"code"},
	})
}

func (f *fakeAS) handleRegister(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.registerCalls++
	clientID := f.issuedClient
	f.mu.Unlock()

	var reqBody map[string]any
	_ = json.NewDecoder(r.Body).Decode(&reqBody)

	w.WriteHeader(http.StatusCreated)
	resp := map[string]any{
		"client_id":                  clientID,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	if ru, ok := reqBody["redirect_uris"]; ok {
		resp["redirect_uris"] = ru
	}
	writeJSON(w, resp)
}

// handleAuthorize эмулирует user-consent: сразу редиректит на redirect_uri
// с code и переданным state. Тесты login дёргают этот URL программно.
func (f *fakeAS) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.authorizeCalls++
	q := map[string]string{}
	for k := range r.URL.Query() {
		q[k] = r.URL.Query().Get(k)
	}
	f.lastAuthQuery = q
	stateOverride := f.authStateOverride
	errParam := f.authErrorParam
	f.mu.Unlock()

	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	if stateOverride != "" {
		state = stateOverride
	}
	if errParam != "" {
		http.Redirect(w, r, redirectURI+"?error="+errParam+"&error_description=denied&state="+state, http.StatusFound)
		return
	}
	http.Redirect(w, r, redirectURI+"?code=auth-code-123&state="+state, http.StatusFound)
}

func (f *fakeAS) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.tokenCalls++
	form := map[string]string{}
	for k := range r.PostForm {
		form[k] = r.PostForm.Get(k)
	}
	f.lastTokenForm = form
	errCode := f.tokenErrorCode
	f.accessCounter++
	access := "access-" + itoa(f.accessCounter)
	refresh := f.issuedRefresh
	if f.rotateRefresh {
		f.issuedRefresh = "refresh-" + itoa(f.accessCounter)
		refresh = f.issuedRefresh
	}
	omitRefresh := f.omitRefreshTok
	grant := form["grant_type"]
	expiresIn := f.expiresInSecond
	f.mu.Unlock()

	if errCode != "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": errCode})
		return
	}

	out := map[string]any{
		"access_token": access,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
	}
	// На refresh-grant AS может не вернуть новый refresh — тогда клиент
	// сохраняет прежний. На authorization_code — refresh обычно есть,
	// если omitRefreshTok не выставлен.
	if !omitRefresh {
		out["refresh_token"] = refresh
	}
	_ = grant
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// itoa — крошечный helper без импорта strconv в hot-path fake.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// oauthTestConfig — config.Config, указывающий на fake AS.
func oauthTestConfig(f *fakeAS) config.Config {
	return config.Config{
		MetabaseURL:            f.URL(),
		AuthMode:               config.AuthModeOAuth,
		OAuthResource:          f.resource(),
		OAuthScopes:            []string{"mb:full"},
		OAuthRedirectAddr:      "127.0.0.1:0",
		OAuthLoginTimeout:      5 * time.Second,
		OAuthResourceOnRefresh: true,
	}
}
