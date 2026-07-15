package metabase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// loginCounter — fake-Metabase, ведёт счётчик POST /api/session.
// Каждый успешный логин возвращает новый session-id (sess-N).
// Опционально перед N-ным запросом возвращает заданный HTTP-статус.
type loginCounter struct {
	mu         sync.Mutex
	calls      int
	failFirstN int    // если > 0 — первые N логинов отвечают failStatus
	failStatus int    // что вернуть на «плохой» попытке
	lastIssued string // последний выданный session-id
}

func (c *loginCounter) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.calls++
		n := c.calls
		c.mu.Unlock()

		if r.Method != http.MethodPost || r.URL.Path != "/api/session" {
			http.NotFound(w, r)
			return
		}
		if c.failFirstN > 0 && n <= c.failFirstN {
			w.WriteHeader(c.failStatus)
			_, _ = w.Write([]byte(`{"errors":{"password":"bad"}}`))
			return
		}
		id := "sess-" + strings.Repeat("x", n)
		c.mu.Lock()
		c.lastIssued = id
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	}
}

func (c *loginCounter) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSession_LazyLogin(t *testing.T) {
	lc := &loginCounter{}
	srv := httptest.NewServer(lc.handler())
	defer srv.Close()

	sm := newSessionManager(srv.URL, "u", "p", srv.Client(), zeroBackoff())
	if lc.Calls() != 0 {
		t.Fatalf("login should be lazy, but %d calls already made", lc.Calls())
	}

	id, err := sm.ensureSession(context.Background())
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if id == "" {
		t.Fatalf("empty session id")
	}
	if lc.Calls() != 1 {
		t.Fatalf("expected 1 login call, got %d", lc.Calls())
	}

	// Повторный вызов — берёт из кэша.
	id2, err := sm.ensureSession(context.Background())
	if err != nil {
		t.Fatalf("ensureSession #2: %v", err)
	}
	if id2 != id {
		t.Fatalf("session changed without invalidation")
	}
	if lc.Calls() != 1 {
		t.Fatalf("expected still 1 login call, got %d", lc.Calls())
	}
}

func TestSession_BadCredentialsNegativeCache(t *testing.T) {
	lc := &loginCounter{failFirstN: 100, failStatus: http.StatusUnauthorized}
	srv := httptest.NewServer(lc.handler())
	defer srv.Close()

	sm := newSessionManager(srv.URL, "u", "bad", srv.Client(), zeroBackoff())
	sm.negCacheTTL = 100 * time.Millisecond

	_, err := sm.ensureSession(context.Background())
	if err == nil {
		t.Fatal("expected auth error")
	}
	// Не больше 1 попытки при 401 (это auth-ошибка, не транзиентная).
	if got := lc.Calls(); got != 1 {
		t.Fatalf("expected 1 login attempt on 401, got %d", got)
	}

	// Сразу пытаемся снова — должны быть отшиты из negative cache.
	_, err = sm.ensureSession(context.Background())
	if err == nil {
		t.Fatal("expected negCache error")
	}
	if got := lc.Calls(); got != 1 {
		t.Fatalf("negCache should suppress retry, but got %d total calls", got)
	}

	// После expiry — снова попытка.
	time.Sleep(150 * time.Millisecond)
	_, err = sm.ensureSession(context.Background())
	if err == nil {
		t.Fatal("expected auth error after negCache expiry")
	}
	if got := lc.Calls(); got != 2 {
		t.Fatalf("expected 2 login calls after negCache expiry, got %d", got)
	}
}

func TestSession_TransientErrorRetried(t *testing.T) {
	lc := &loginCounter{failFirstN: 2, failStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(lc.handler())
	defer srv.Close()

	sm := newSessionManager(srv.URL, "u", "p", srv.Client(), zeroBackoff())

	id, err := sm.ensureSession(context.Background())
	if err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	// 2 fail + 1 success = 3 calls.
	if got := lc.Calls(); got != 3 {
		t.Fatalf("expected 3 calls (2 retries), got %d", got)
	}
}

// newReq — throwaway-запрос для вызова apply в тестах.
func newReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://example/api/database", nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	return req
}

// TestSession_ConcurrentInvalidateRace — ключевой тест из плана.
// 50 goroutines одновременно держат снимок generation первой сессии и зовут
// invalidate(gen)+apply. Ожидание: ровно 2 логина (initial + один re-login).
// Если invalidate() была бы безусловной, каждая горутина со старым gen
// сбрасывала бы чью-то новую сессию и провоцировала ещё один логин.
func TestSession_ConcurrentInvalidateRace(t *testing.T) {
	lc := &loginCounter{}
	srv := httptest.NewServer(lc.handler())
	defer srv.Close()

	sm := newSessionManager(srv.URL, "u", "p", srv.Client(), zeroBackoff())

	// Шаг 1: получить первую сессию и её generation.
	gen0, err := sm.apply(context.Background(), newReq(t))
	if err != nil {
		t.Fatalf("initial login: %v", err)
	}
	if lc.Calls() != 1 {
		t.Fatalf("setup: %d logins", lc.Calls())
	}

	const N = 50
	var wg sync.WaitGroup
	var invalidates atomic.Int32

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = sm.invalidate(context.Background(), gen0)
			invalidates.Add(1)
			if _, err := sm.apply(context.Background(), newReq(t)); err != nil {
				t.Errorf("apply in goroutine: %v", err)
			}
		}()
	}
	wg.Wait()

	// Ровно один re-login допустим. Initial + relogin = 2.
	calls := lc.Calls()
	if calls != 2 {
		t.Fatalf("expected exactly 2 logins (1 initial + 1 relogin), got %d", calls)
	}
}

func TestSession_InvalidateOnlyOnMatch(t *testing.T) {
	lc := &loginCounter{}
	srv := httptest.NewServer(lc.handler())
	defer srv.Close()

	sm := newSessionManager(srv.URL, "u", "p", srv.Client(), zeroBackoff())

	gen1, err := sm.apply(context.Background(), newReq(t))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	// Инвалидируем «чужой» generation. Текущая сессия не должна слететь.
	if err := sm.invalidate(context.Background(), gen1+999); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	gen2, err := sm.apply(context.Background(), newReq(t))
	if err != nil {
		t.Fatalf("login 2: %v", err)
	}
	if gen1 != gen2 {
		t.Fatalf("session should not be invalidated when generation does not match")
	}
	if got := lc.Calls(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

// zeroBackoff — нулевые задержки между попытками, чтобы тесты не висели.
func zeroBackoff() []time.Duration {
	return []time.Duration{0, 0, 0}
}
