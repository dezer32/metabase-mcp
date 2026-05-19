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
)

// Client — HTTP-клиент к Metabase. Знает про сессию и 401-retry.
type Client struct {
	baseURL string
	session *sessionManager
	http    *http.Client
	log     *slog.Logger
}

// NewClient собирает клиент с дефолтными параметрами:
// HTTP-timeout, 3 backoff-попытки логина (500ms/1s/2s), neg-cache 30s.
func NewClient(baseURL, user, password string, httpTimeout time.Duration, log *slog.Logger) *Client {
	hc := &http.Client{Timeout: httpTimeout}
	return &Client{
		baseURL: baseURL,
		session: newSessionManager(baseURL, user, password, hc, defaultBackoffs()),
		http:    hc,
		log:     log,
	}
}

// doJSON — основной метод запроса к Metabase.
// path: "/api/database", "/api/dataset" и т.п.
// method: GET/POST.
// body: будет сериализован в JSON, или nil.
// out: указатель на структуру для декодирования, или nil если не интересует.
// Логика 401: если получили 401 — инвалидируем сессию, пробуем ещё один раз.
// Если снова 401 — возвращаем ошибку наружу.
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	for attempt := 0; ; attempt++ {
		sessID, err := c.session.ensureSession(ctx)
		if err != nil {
			return fmt.Errorf("session: %w", err)
		}

		status, raw, err := c.roundTrip(ctx, method, path, sessID, body)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized {
			// Сессия истекла на стороне Metabase. Сбрасываем и пробуем ещё раз.
			c.session.invalidate(sessID)
			if attempt == 0 {
				c.log.Debug("metabase: 401, retrying with fresh session",
					slog.String("path", path))
				continue
			}
			return fmt.Errorf("metabase: %s %s — repeated 401 after relogin", method, path)
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

// roundTrip — собирает запрос с X-Metabase-Session и делает один HTTP-вызов.
// Возвращает (status, body, err). err только на сетевых/протокольных проблемах.
func (c *Client) roundTrip(ctx context.Context, method, path, sessID string, body any) (int, []byte, error) {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encode body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("build req: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Metabase-Session", sessID)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("transport: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, raw, nil
}
