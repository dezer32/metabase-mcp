package metabase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/dezer32/metabase-mcp/internal/metabase/oauth"
)

// Client — HTTP-клиент к Metabase. Знает про аутентификацию (session или
// oauth) и 401-retry.
type Client struct {
	baseURL string
	auth    authProvider
	http    *http.Client
	log     *slog.Logger
}

// newClient — общий конструктор: связывает baseURL, провайдер аутентификации,
// HTTP-клиент и логгер.
func newClient(baseURL string, auth authProvider, hc *http.Client, log *slog.Logger) *Client {
	return &Client{baseURL: baseURL, auth: auth, http: hc, log: log}
}

// NewClient собирает password-клиент: HTTP-timeout, 4 backoff-попытки логина
// (0/500ms/1s/2s), neg-cache 30s.
func NewClient(baseURL, user, password string, httpTimeout time.Duration, log *slog.Logger) *Client {
	hc := &http.Client{Timeout: httpTimeout}
	return newClient(baseURL, newSessionManager(baseURL, user, password, hc, defaultBackoffs()), hc, log)
}

// NewOAuthClient собирает oauth-клиент поверх готового oauth.Manager.
// hc — тот же bounded HTTP-клиент, что используется для token-операций.
func NewOAuthClient(baseURL string, mgr *oauth.Manager, hc *http.Client, log *slog.Logger) *Client {
	return newClient(baseURL, &oauthProvider{mgr: mgr}, hc, log)
}

// doJSON — основной метод запроса к Metabase.
// path: "/api/database", "/api/dataset" и т.п.
// method: GET/POST.
// body: будет сериализован в JSON, или nil.
// out: указатель на структуру для декодирования, или nil если не интересует.
//
// Логика 401 (см. classify401): истёкший токен/сессия (invalid_token или нет
// заголовка) → invalidate + ровно один ретрай; при ошибке инвалидизации —
// возвращаем её (не ретраим со старым credential). insufficient_scope/audience
// → диагностическая ошибка без рефреша.
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	for attempt := 0; ; attempt++ {
		status, raw, header, cred, err := c.roundTrip(ctx, method, path, body)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized {
			retryable, diag := classify401(header)
			if attempt == 0 && retryable {
				if ierr := c.auth.invalidate(ctx, cred); ierr != nil {
					return fmt.Errorf("metabase: %s %s — 401 and credential refresh failed: %w",
						method, path, ierr)
				}
				c.log.Debug("metabase: 401, retrying with refreshed credential",
					slog.String("path", path))
				continue
			}
			if !retryable {
				return fmt.Errorf("metabase: %s %s — 401 not retryable (%s); body=%s",
					method, path, diag, truncate(string(raw), 300))
			}
			return fmt.Errorf("metabase: %s %s — repeated 401 after credential refresh; body=%s",
				method, path, truncate(string(raw), 300))
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("metabase: %s %s — status=%d body=%s",
				method, path, status, truncate(string(raw), 300))
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("metabase: decode %s %s: %w", method, path, err)
			}
		}
		return nil
	}
}

// roundTrip собирает запрос, ставит auth-заголовок через провайдера и делает
// один HTTP-вызов. Возвращает (status, body, headers, cred, err); cred —
// generation credential'а, использованного для этого запроса (для invalidate).
func (c *Client) roundTrip(ctx context.Context, method, path string, body any) (int, []byte, http.Header, uint64, error) {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, 0, fmt.Errorf("encode body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return 0, nil, nil, 0, fmt.Errorf("build req: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	cred, err := c.auth.apply(ctx, req)
	if err != nil {
		return 0, nil, nil, 0, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, 0, fmt.Errorf("transport: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, 0, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, raw, resp.Header, cred, nil
}
