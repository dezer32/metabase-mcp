//go:build integration

// Package test содержит integration-тесты для metabase-mcp.
// Запускаются под build-tag integration.
package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
)

// FakeMetabase — минимальный fake-сервер Metabase API.
// Отдаёт: POST /api/session, GET /api/database, GET /api/database/:id/metadata,
// POST /api/dataset.
type FakeMetabase struct {
	srv          *httptest.Server
	loginCalls   atomic.Int32
	sessionToken atomic.Value // string

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
	if !f.checkSession(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": f.databases})
}

func (f *FakeMetabase) handleMetadata(w http.ResponseWriter, r *http.Request) {
	if !f.checkSession(w, r) {
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
	if !f.checkSession(w, r) {
		return
	}
	f.mu.Lock()
	payload := f.dataset
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (f *FakeMetabase) checkSession(w http.ResponseWriter, r *http.Request) bool {
	got := r.Header.Get("X-Metabase-Session")
	cur, _ := f.sessionToken.Load().(string)
	if got == "" || got != cur {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

