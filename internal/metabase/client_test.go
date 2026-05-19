package metabase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dezer32/metabase-mcp/internal/logging"
)

// newTestClient — клиент к fake-Metabase srv. backoffs = zeroBackoff() для тестов.
func newTestClient(srv *httptest.Server) *Client {
	hc := srv.Client()
	c := &Client{
		baseURL: srv.URL,
		session: newSessionManager(srv.URL, "u", "p", hc, []time.Duration{0, 0, 0}),
		http:    hc,
		log:     logging.Discard(),
	}
	return c
}

// fakeMux — упрощённый fake-Metabase для client-тестов.
type fakeMux struct {
	loginCalls   atomic.Int32
	dataCalls    atomic.Int32
	currentToken atomic.Value // string
	expireOnce   atomic.Bool  // если true — первый запрос с currentToken вернёт 401
}

func (f *fakeMux) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			n := f.loginCalls.Add(1)
			id := "tok-" + string(rune('A'+n-1))
			f.currentToken.Store(id)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id})

		case r.URL.Path == "/api/database":
			f.dataCalls.Add(1)
			sess := r.Header.Get("X-Metabase-Session")
			cur, _ := f.currentToken.Load().(string)
			if sess == "" || sess != cur {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if f.expireOnce.CompareAndSwap(true, false) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": 3, "name": "meta_helpdesk", "engine": "mysql"}},
			})

		default:
			http.NotFound(w, r)
		}
	}
}

func TestClient_DoJSON_HappyPath(t *testing.T) {
	f := &fakeMux{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	c := newTestClient(srv)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := c.doJSON(context.Background(), http.MethodGet, "/api/database", nil, &resp); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0]["name"] != "meta_helpdesk" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if f.loginCalls.Load() != 1 {
		t.Fatalf("expected 1 login, got %d", f.loginCalls.Load())
	}
}

func TestClient_DoJSON_401Retries(t *testing.T) {
	f := &fakeMux{}
	f.expireOnce.Store(true)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	c := newTestClient(srv)
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := c.doJSON(context.Background(), http.MethodGet, "/api/database", nil, &resp); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	// Должен быть 1 initial login, 1 fail на data, 1 relogin, 1 success на data.
	if f.loginCalls.Load() != 2 {
		t.Fatalf("expected 2 login calls (initial + relogin), got %d", f.loginCalls.Load())
	}
	if f.dataCalls.Load() != 2 {
		t.Fatalf("expected 2 data calls (failed + retried), got %d", f.dataCalls.Load())
	}
}

func TestClient_DoJSON_Repeated401Fails(t *testing.T) {
	// Эмулируем мок, который всегда возвращает 401 на data.
	calls := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/session" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess"})
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.doJSON(context.Background(), http.MethodGet, "/api/database", nil, nil)
	if err == nil {
		t.Fatal("expected error on repeated 401")
	}
	if !strings.Contains(err.Error(), "repeated 401") {
		t.Errorf("error should mention repeated 401: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 data attempts, got %d", got)
	}
}

func TestClient_DoJSON_PassesBody(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/session" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess"})
			return
		}
		if r.URL.Path == "/api/dataset" && r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&seen)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"completed"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	body := map[string]any{"database": 3, "type": "native"}
	if err := c.doJSON(context.Background(), http.MethodPost, "/api/dataset", body, nil); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	if got := seen["database"]; got != float64(3) {
		t.Errorf("body not passed: %+v", seen)
	}
}
