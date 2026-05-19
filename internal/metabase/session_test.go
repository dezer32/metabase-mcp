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

// TestSession_ConcurrentInvalidateRace — ключевой тест из плана.
// 50 goroutines одновременно держат старую сессию sess-X.
// Fake возвращает 401 на любой запрос с sess-X, 200 после re-login.
// Ожидание: ровно 2 логина (initial + один re-login), не 51.
// Если invalidate() будет безусловным, увидим много логинов: каждая горутина
// со старой сессией будет сбрасывать чью-то новую и провоцировать ещё один.
func TestSession_ConcurrentInvalidateRace(t *testing.T) {
	lc := &loginCounter{}
	srv := httptest.NewServer(lc.handler())
	defer srv.Close()

	sm := newSessionManager(srv.URL, "u", "p", srv.Client(), zeroBackoff())

	// Шаг 1: получить первую сессию.
	first, err := sm.ensureSession(context.Background())
	if err != nil {
		t.Fatalf("initial login: %v", err)
	}
	if lc.Calls() != 1 {
		t.Fatalf("setup: %d logins", lc.Calls())
	}

	const N = 50
	var wg sync.WaitGroup
	var invalidates atomic.Int32

	// Все горутины вызывают invalidate(first) — как будто получили 401
	// с использованием старой сессии. Потом сразу ensureSession.
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			sm.invalidate(first)
			invalidates.Add(1)
			id, err := sm.ensureSession(context.Background())
			if err != nil {
				t.Errorf("ensureSession in goroutine: %v", err)
				return
			}
			if id == "" {
				t.Errorf("empty id")
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

	id1, err := sm.ensureSession(context.Background())
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	// Инвалидируем «чужую» сессию. Текущая не должна слететь.
	sm.invalidate("some-old-id-we-never-had")
	id2, err := sm.ensureSession(context.Background())
	if err != nil {
		t.Fatalf("login 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("session should not be invalidated when ID does not match")
	}
	if got := lc.Calls(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

// zeroBackoff — нулевые задержки между попытками, чтобы тесты не висели.
func zeroBackoff() []time.Duration {
	return []time.Duration{0, 0, 0}
}
