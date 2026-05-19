package metabase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ErrAuth — кред неправильный. Возвращается из ensureSession при 401/400.
// Помечает «known bad», на короткое время не пытаемся снова логиниться,
// чтобы не словить rate-limit Metabase.
var ErrAuth = errors.New("metabase: authentication failed")

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// defaultNegCacheTTL — окно, в течение которого после 401/400 от Metabase
// мы не пытаемся снова логиниться, чтобы не словить rate-limit.
const defaultNegCacheTTL = 30 * time.Second

// sessionManager управляет сессионным cookie Metabase.
// Lazy: первый login откладывается до первого ensureSession.
// Thread-safe: при параллельных вызовах логин выполняется ровно одной
// горутиной, остальные ждут результата и читают из кэша.
type sessionManager struct {
	baseURL  string
	user     string
	password string
	http     *http.Client
	backoffs []time.Duration

	// mu защищает cached и negCacheUntil. Удерживается на время doLogin,
	// чтобы исключить «гонку логинов»: 50 параллельных вызовов
	// ensureSession при пустом кэше выполнят один doLogin, остальные
	// получат результат из double-check.
	mu            sync.Mutex
	cached        string
	negCacheUntil time.Time
	negCacheTTL   time.Duration
}

// newSessionManager — внутренний конструктор для тестов и прод-кода.
func newSessionManager(baseURL, user, password string, hc *http.Client, backoffs []time.Duration) *sessionManager {
	return &sessionManager{
		baseURL:     baseURL,
		user:        user,
		password:    password,
		http:        hc,
		backoffs:    backoffs,
		negCacheTTL: defaultNegCacheTTL,
	}
}

// ensureSession возвращает действующий session-id.
// Если в кэше пусто — логинится с backoff. На 401/400 — фиксирует
// known-bad и в течение negCacheTTL не пытается снова.
func (s *sessionManager) ensureSession(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check после захвата лока: возможно, пока мы ждали Lock,
	// соседняя горутина уже залогинилась.
	if s.cached != "" {
		return s.cached, nil
	}
	if time.Now().Before(s.negCacheUntil) {
		return "", ErrAuth
	}

	var lastErr error
	for i, d := range s.backoffs {
		if i > 0 && d > 0 {
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return "", ctx.Err()
			case <-t.C:
			}
		}
		id, err := s.doLogin(ctx)
		if err == nil {
			s.cached = id
			return id, nil
		}
		if errors.Is(err, ErrAuth) {
			s.negCacheUntil = time.Now().Add(s.negCacheTTL)
			return "", err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("metabase: login failed without specific error")
	}
	return "", lastErr
}

// invalidate сбрасывает кэшированную сессию ТОЛЬКО если она совпадает с
// переданной. Это критично для race-safety: если параллельная горутина уже
// успела перелогиниться и положила в кэш свежую сессию, наш просроченный
// 401 не должен её сбрасывать.
func (s *sessionManager) invalidate(usedID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached == usedID {
		s.cached = ""
	}
}

// doLogin делает POST /api/session. Возвращает ErrAuth на 401/400,
// fmt.Errorf с контекстом — на остальные сетевые/протокольные ошибки.
// Вызывается под удержанием mu.
func (s *sessionManager) doLogin(ctx context.Context) (string, error) {
	body, err := json.Marshal(loginRequest{Username: s.user, Password: s.password})
	if err != nil {
		return "", fmt.Errorf("encode login: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/api/session", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build login req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("login transport: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("login: read body: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
		return "", fmt.Errorf("%w: status=%d body=%s",
			ErrAuth, resp.StatusCode, truncate(string(raw), 200))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("login: unexpected status %d: %s",
			resp.StatusCode, truncate(string(raw), 200))
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("login: decode: %w", err)
	}
	if out.ID == "" {
		return "", errors.New("login: empty session id in response")
	}
	return out.ID, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// defaultBackoffs — задержки между попытками: 500ms → 1s → 2s.
// Всего 4 попытки (первая — без задержки).
func defaultBackoffs() []time.Duration {
	return []time.Duration{0, 500 * time.Millisecond, 1 * time.Second, 2 * time.Second}
}
