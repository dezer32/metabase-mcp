//go:build integration

// Package test содержит integration-тесты для metabase-mcp.
// Запускаются под build-tag integration.
package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// FakeMetabase — минимальный fake-сервер Metabase API + OAuth authorization
// server. Отдаёт: POST /api/session, GET /api/database,
// GET /api/database/:id/metadata, POST /api/dataset, а также well-known
// discovery, /oauth/register, /oauth/authorize, /oauth/token. Data-эндпоинты
// принимают ЛИБО X-Metabase-Session (session-режим), ЛИБО Authorization: Bearer
// (oauth-режим).
type FakeMetabase struct {
	srv          *httptest.Server
	loginCalls   atomic.Int32
	sessionToken atomic.Value // string

	tokenCalls   atomic.Int32
	issuedAccess atomic.Value // string — последний выданный Bearer

	mu           sync.Mutex
	databases    []map[string]any
	metadataByDB map[int]map[string]any
	dataset      map[string]any
}

// NewFakeMetabase запускает фейковый сервер.
// По умолчанию возвращает meta_helpdesk (id=3) и hydra (id=5),
// пустой metadata, и успешный dataset с двумя строками.
func NewFakeMetabase() *FakeMetabase {
	f := &FakeMetabase{
		databases: []map[string]any{
			{"id": 3, "name": "meta_helpdesk", "engine": "mysql"},
			{"id": 5, "name": "hydra", "engine": "mysql"},
		},
		metadataByDB: map[int]map[string]any{
			3: {"tables": []map[string]any{
				{
					"id":     1,
					"name":   "tickets",
					"schema": "public",
					"fields": []map[string]any{
						{"id": 10, "name": "id", "base_type": "type/Integer", "database_required": true},
						{"id": 11, "name": "subject", "base_type": "type/Text"},
					},
				},
			}},
			5: {"tables": []map[string]any{
				{
					"id":     2,
					"name":   "employees",
					"schema": "public",
					"fields": []map[string]any{
						{"id": 20, "name": "id", "base_type": "type/Integer"},
					},
				},
			}},
		},
		dataset: map[string]any{
			"status":       "completed",
			"running_time": 7,
			"data": map[string]any{
				"cols": []map[string]any{{"name": "n", "base_type": "type/Integer"}},
				"rows": [][]any{{1}, {2}, {3}},
			},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session", f.handleLogin)
	mux.HandleFunc("GET /api/database", f.handleDatabases)
	mux.HandleFunc("GET /api/database/{id}/metadata", f.handleMetadata)
	mux.HandleFunc("POST /api/dataset", f.handleDataset)
	// OAuth authorization server + protected resource.
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", f.handlePRM)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", f.handleASMeta)
	mux.HandleFunc("POST /oauth/register", f.handleOAuthRegister)
	mux.HandleFunc("GET /oauth/authorize", f.handleOAuthAuthorize)
	mux.HandleFunc("POST /oauth/token", f.handleOAuthToken)
	f.srv = httptest.NewServer(mux)
	return f
}

// URL возвращает базовый URL для METABASE_URL.
func (f *FakeMetabase) URL() string { return f.srv.URL }

// Close останавливает сервер.
func (f *FakeMetabase) Close() { f.srv.Close() }

func (f *FakeMetabase) handleLogin(w http.ResponseWriter, _ *http.Request) {
	n := f.loginCalls.Add(1)
	id := "tok-" + string(rune('A'+n-1))
	f.sessionToken.Store(id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func (f *FakeMetabase) handleDatabases(w http.ResponseWriter, r *http.Request) {
	if !f.checkAuth(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": f.databases})
}

func (f *FakeMetabase) handleMetadata(w http.ResponseWriter, r *http.Request) {
	if !f.checkAuth(w, r) {
		return
	}
	dbID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	meta, ok := f.metadataByDB[dbID]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "no such db", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}

func (f *FakeMetabase) handleDataset(w http.ResponseWriter, r *http.Request) {
	if !f.checkAuth(w, r) {
		return
	}
	f.mu.Lock()
	payload := f.dataset
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// checkAuth принимает ЛИБО валидный X-Metabase-Session, ЛИБО Authorization:
// Bearer с последним выданным access-токеном.
func (f *FakeMetabase) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") {
		tok := strings.TrimPrefix(authz, "Bearer ")
		cur, _ := f.issuedAccess.Load().(string)
		if tok != "" && tok == cur {
			return true
		}
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	got := r.Header.Get("X-Metabase-Session")
	cur, _ := f.sessionToken.Load().(string)
	if got == "" || got != cur {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func (f *FakeMetabase) handlePRM(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"resource":                 f.srv.URL + "/api/metabase-mcp",
		"authorization_servers":    []string{f.srv.URL},
		"scopes_supported":         []string{"mb:full"},
		"bearer_methods_supported": []string{"header"},
	})
}

func (f *FakeMetabase) handleASMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                f.srv.URL,
		"authorization_endpoint":                f.srv.URL + "/oauth/authorize",
		"token_endpoint":                        f.srv.URL + "/oauth/token",
		"registration_endpoint":                 f.srv.URL + "/oauth/register",
		"scopes_supported":                      []string{"mb:full"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"response_types_supported":              []string{"code"},
	})
}

func (f *FakeMetabase) handleOAuthRegister(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{
		"client_id":                  "e2e-client",
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

func (f *FakeMetabase) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	http.Redirect(w, r, redirectURI+"?code=e2e-code&state="+state, http.StatusFound)
}

func (f *FakeMetabase) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	n := f.tokenCalls.Add(1)
	access := "at-" + strconv.Itoa(int(n))
	refresh := "rt-" + strconv.Itoa(int(n))
	f.issuedAccess.Store(access)
	writeJSON(w, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": refresh,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
