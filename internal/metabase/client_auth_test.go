package metabase

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dezer32/metabase-mcp/internal/logging"
)

// fakeProvider — управляемый authProvider для проверки 401-логики doJSON.
type fakeProvider struct {
	mu              sync.Mutex
	applyCalls      int
	invalidateCalls int
	invalidateErr   error
	gen             uint64
}

func (p *fakeProvider) apply(_ context.Context, req *http.Request) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyCalls++
	req.Header.Set("Authorization", "Bearer test")
	return p.gen, nil
}

func (p *fakeProvider) invalidate(_ context.Context, _ uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invalidateCalls++
	if p.invalidateErr != nil {
		return p.invalidateErr
	}
	p.gen++
	return nil
}

func (p *fakeProvider) applies() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.applyCalls
}

func (p *fakeProvider) invalidations() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.invalidateCalls
}

func TestDoJSON_OAuth401_InvalidTokenRefreshes(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="expired"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	fp := &fakeProvider{}
	c := newClient(srv.URL, fp, srv.Client(), logging.Discard())

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.doJSON(context.Background(), http.MethodGet, "/api/database", nil, &out); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	if !out.OK {
		t.Error("expected ok=true after refresh+retry")
	}
	if fp.invalidations() != 1 {
		t.Errorf("expected 1 invalidate on invalid_token, got %d", fp.invalidations())
	}
	if fp.applies() != 2 {
		t.Errorf("expected 2 applies (initial + retry), got %d", fp.applies())
	}
}

func TestDoJSON_OAuth401_InsufficientScopeNoRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", error_description="need mb:full"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	fp := &fakeProvider{}
	c := newClient(srv.URL, fp, srv.Client(), logging.Discard())

	err := c.doJSON(context.Background(), http.MethodGet, "/api/database", nil, nil)
	if err == nil {
		t.Fatal("expected error on insufficient_scope")
	}
	if !strings.Contains(err.Error(), "insufficient_scope") {
		t.Errorf("error should mention insufficient_scope: %v", err)
	}
	if fp.invalidations() != 0 {
		t.Errorf("insufficient_scope must NOT trigger refresh, got %d invalidations", fp.invalidations())
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 data call (no retry), got %d", got)
	}
}

func TestDoJSON_OAuth401_RefreshFails(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	fp := &fakeProvider{invalidateErr: errors.New("refresh boom")}
	c := newClient(srv.URL, fp, srv.Client(), logging.Discard())

	err := c.doJSON(context.Background(), http.MethodGet, "/api/database", nil, nil)
	if err == nil {
		t.Fatal("expected error when refresh fails")
	}
	if !strings.Contains(err.Error(), "refresh boom") {
		t.Errorf("error should propagate refresh failure: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("must not retry with stale credential after failed refresh, got %d data calls", got)
	}
}
