package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dezer32/metabase-mcp/internal/config"
	"golang.org/x/oauth2"
)

// Manager владеет текущим токеном и умеет тихо рефрешить его, ротируя
// refresh-токен и атомарно сохраняя на диск.
//
// Инвалидизация по 401 сделана через generation: apply отдаёт снимок
// (токен + gen), а ForceRefresh рефрешит ТОЛЬКО если переданный gen совпадает
// с текущим. Так «протухший 401» от старого access-токена не форс-рефрешит
// уже обновлённый (см. sessionManager.invalidate — та же семантика).
type Manager struct {
	cfg        config.Config
	meta       *Meta
	hc         *http.Client
	log        *slog.Logger
	clientAuth ClientAuth
	tokenPath  string

	mu          sync.Mutex
	tok         *oauth2.Token
	gen         uint64
	redirectURI string
}

// NewTokenSource поднимает Manager: discovery → (пригодный сохранённый токен?
// seed'им им) → иначе интерактивный вход (DCR при необходимости) и персист.
//
// hc — bounded HTTP-клиент; он же должен лежать в ctx под ключом
// oauth2.HTTPClient (для Exchange внутри InteractiveLogin).
func NewTokenSource(ctx context.Context, cfg config.Config, hc *http.Client, log *slog.Logger) (*Manager, error) {
	meta, err := Discover(ctx, cfg, hc)
	if err != nil {
		return nil, err
	}
	warnUnknownScopes(cfg.OAuthScopes, meta.ScopesSupported, log)

	m := &Manager{cfg: cfg, meta: meta, hc: hc, log: log, tokenPath: cfg.TokenFile}

	// Пробуем сохранённый токен.
	if rec, err := LoadRecord(cfg.TokenFile); err == nil {
		switch {
		case !recordMatches(rec, cfg, meta):
			log.Info("oauth: saved token does not match current config, re-authenticating",
				slog.String("token_file", cfg.TokenFile))
		case rec.RefreshToken == "":
			log.Info("oauth: saved token has no refresh token, re-authenticating")
		default:
			ca, err := buildClientAuth(cfg, meta, rec.ClientID)
			if err != nil {
				return nil, err
			}
			m.clientAuth = ca
			m.redirectURI = rec.RedirectURI
			// Seed'им ПОЛНЫМ токеном: живой access переиспользуем, истёкший —
			// повод для тихого refresh при первом Token(), а не для интерактива.
			m.tok = &oauth2.Token{
				AccessToken:  rec.AccessToken,
				RefreshToken: rec.RefreshToken,
				TokenType:    "Bearer",
				Expiry:       rec.Expiry,
			}
			log.Info("oauth: loaded persisted token", slog.String("token_file", cfg.TokenFile))
			return m, nil
		}
	}

	// Нет пригодного токена — нужен интерактивный вход.
	if cfg.OAuthNoninteractive {
		return nil, fmt.Errorf("oauth: no usable saved token at %s and "+
			"METABASE_OAUTH_NONINTERACTIVE=true; run once interactively to authorize", cfg.TokenFile)
	}

	lis, redirectURI, err := BindCallback(cfg.OAuthRedirectAddr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lis.Close() }()

	var clientAuth ClientAuth
	if cfg.OAuthClientID == "" {
		// Публичный клиент через Dynamic Client Registration.
		resp, err := Register(ctx, meta, redirectURI, cfg.OAuthScopes, hc)
		if err != nil {
			return nil, err
		}
		clientAuth = ClientAuth{ClientID: resp.ClientID, Mode: "public", AuthStyle: oauth2.AuthStyleInParams}
	} else {
		clientAuth, err = buildClientAuth(cfg, meta, "")
		if err != nil {
			return nil, err
		}
	}
	m.clientAuth = clientAuth
	m.redirectURI = redirectURI

	loginCtx, cancel := context.WithTimeout(ctx, cfg.OAuthLoginTimeout)
	defer cancel()
	tok, err := InteractiveLogin(loginCtx, meta, clientAuth, lis, redirectURI,
		cfg.OAuthScopes, meta.Resource, log)
	if err != nil {
		return nil, err
	}
	m.tok = tok
	m.gen = 0
	if err := m.saveLocked(); err != nil {
		log.Warn("oauth: failed to persist token after login", slog.Any("err", err))
	}
	return m, nil
}

// Token возвращает снимок (токен + generation) под одним мьютексом.
// Если текущий токен истёк — синхронно рефрешит под тем же локом.
func (m *Manager) Token(ctx context.Context) (*oauth2.Token, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tok.Valid() {
		return m.tok, m.gen, nil
	}
	if err := m.doRefreshLocked(ctx); err != nil {
		return nil, m.gen, err
	}
	return m.tok, m.gen, nil
}

// ForceRefresh условно рефрешит: только если gen совпадает с текущим (иначе
// параллельная горутина уже обновила токен). Поднимает generation и атомарно
// сохраняет. Ошибку рефреша пробрасывает наверх.
func (m *Manager) ForceRefresh(ctx context.Context, gen uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != m.gen {
		// Кто-то уже обновил токен после того, как вызвавший снял credential.
		return nil
	}
	return m.doRefreshLocked(ctx)
}

// doRefreshLocked делает реальный refresh, ротирует refresh-токен (сохраняя
// прежний, если AS не вернул новый), поднимает generation и атомарно
// сохраняет. Вызывается под удержанием mu.
func (m *Manager) doRefreshLocked(ctx context.Context) error {
	if m.tok == nil || m.tok.RefreshToken == "" {
		return fmt.Errorf("oauth: cannot refresh: no refresh token")
	}
	newTok, err := m.refreshToken(ctx, m.tok.RefreshToken)
	if err != nil {
		return err
	}
	// Ротация: x/oauth2-семантика — если AS не вернул новый refresh, оставляем прежний.
	if newTok.RefreshToken == "" {
		newTok.RefreshToken = m.tok.RefreshToken
	}
	m.tok = newTok
	m.gen++
	if err := m.saveLocked(); err != nil {
		m.log.Warn("oauth: failed to persist refreshed token", slog.Any("err", err))
	}
	return nil
}

// refreshToken выполняет POST grant_type=refresh_token напрямую (не через
// x/oauth2 TokenSource), чтобы иметь возможность добавить RFC 8707 resource
// и применить нужный способ client-auth. Возвращает новый токен.
func (m *Manager) refreshToken(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if m.cfg.OAuthResourceOnRefresh {
		form.Set("resource", m.meta.Resource)
	}
	// Публичный клиент: client_id в теле. Confidential+post: id+secret в теле.
	// Confidential+basic: заголовок Authorization (ниже), в тело id/secret не кладём.
	basic := m.clientAuth.Mode == "confidential" && m.clientAuth.AuthStyle == oauth2.AuthStyleInHeader
	if !basic {
		if m.clientAuth.ClientID != "" {
			form.Set("client_id", m.clientAuth.ClientID)
		}
		if m.clientAuth.ClientSecret != "" {
			form.Set("client_secret", m.clientAuth.ClientSecret)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.meta.TokenEP,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(m.clientAuth.ClientID), url.QueryEscape(m.clientAuth.ClientSecret))
	}

	hc := m.hc
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: refresh transport: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: read refresh response: %w", err)
	}

	var tj struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tj); err != nil {
		return nil, fmt.Errorf("oauth: decode refresh response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || tj.Error != "" {
		return nil, fmt.Errorf("oauth: refresh failed (status %d): %s %s",
			resp.StatusCode, tj.Error, tj.ErrorDesc)
	}
	if tj.AccessToken == "" {
		return nil, fmt.Errorf("oauth: refresh response has empty access_token")
	}

	tokType := tj.TokenType
	if tokType == "" {
		tokType = "Bearer"
	}
	var expiry time.Time
	if tj.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(tj.ExpiresIn) * time.Second)
	}
	return &oauth2.Token{
		AccessToken:  tj.AccessToken,
		TokenType:    tokType,
		RefreshToken: tj.RefreshToken,
		Expiry:       expiry,
	}, nil
}

// saveLocked сериализует текущее состояние в Record и атомарно пишет на диск.
// Вызывается под удержанием mu.
func (m *Manager) saveLocked() error {
	rec := &Record{
		Issuer:        m.meta.Issuer,
		Resource:      m.meta.Resource,
		TokenEndpoint: m.meta.TokenEP,
		ClientID:      m.clientAuth.ClientID,
		RedirectURI:   m.redirectURI,
		Scopes:        m.cfg.OAuthScopes,
		ClientMode:    m.clientAuth.Mode,
		RefreshToken:  m.tok.RefreshToken,
		AccessToken:   m.tok.AccessToken,
		Expiry:        m.tok.Expiry,
	}
	return SaveRecord(m.tokenPath, rec)
}

// buildClientAuth решает, как клиент аутентифицируется на token endpoint.
// dcrClientID используется только когда env-CLIENT_ID не задан (публичный
// клиент из DCR или из сохранённого record).
func buildClientAuth(cfg config.Config, meta *Meta, dcrClientID string) (ClientAuth, error) {
	if cfg.OAuthClientID != "" {
		ca := ClientAuth{ClientID: cfg.OAuthClientID}
		if cfg.OAuthClientSecret == "" {
			ca.Mode = "public"
			ca.AuthStyle = oauth2.AuthStyleInParams
			return ca, nil
		}
		ca.ClientSecret = cfg.OAuthClientSecret
		ca.Mode = "confidential"
		style, err := pickConfidentialStyle(meta.TokenEndpointAuthMethods)
		if err != nil {
			return ClientAuth{}, err
		}
		ca.AuthStyle = style
		return ca, nil
	}
	return ClientAuth{ClientID: dcrClientID, Mode: "public", AuthStyle: oauth2.AuthStyleInParams}, nil
}

// pickConfidentialStyle выбирает способ client-auth для confidential-клиента.
// По OAuth 2.1 предпочитаем client_secret_post; иначе client_secret_basic.
func pickConfidentialStyle(methods []string) (oauth2.AuthStyle, error) {
	switch {
	case slices.Contains(methods, "client_secret_post"):
		return oauth2.AuthStyleInParams, nil
	case slices.Contains(methods, "client_secret_basic"):
		return oauth2.AuthStyleInHeader, nil
	default:
		return oauth2.AuthStyleAutoDetect, fmt.Errorf("oauth: confidential client requested but authorization "+
			"server supports neither client_secret_post nor client_secret_basic (methods=%v)", methods)
	}
}

// recordMatches проверяет, что сохранённый токен относится к текущему
// контексту: issuer/resource/token_endpoint/scopes/client_mode совпадают,
// а при заданном env-CLIENT_ID — и client_id.
func recordMatches(rec *Record, cfg config.Config, meta *Meta) bool {
	if rec.Issuer != meta.Issuer || rec.Resource != meta.Resource || rec.TokenEndpoint != meta.TokenEP {
		return false
	}
	if !scopesEqual(rec.Scopes, cfg.OAuthScopes) {
		return false
	}
	expectedMode := "public"
	if cfg.OAuthClientSecret != "" {
		expectedMode = "confidential"
	}
	if rec.ClientMode != expectedMode {
		return false
	}
	if cfg.OAuthClientID != "" && rec.ClientID != cfg.OAuthClientID {
		return false
	}
	return true
}

func scopesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// warnUnknownScopes логирует warning, если запрошены scope'ы, которых нет в
// объявленных сервером (scopes_supported может быть неполным — поэтому warn,
// а не ошибка).
func warnUnknownScopes(requested, supported []string, log *slog.Logger) {
	if len(supported) == 0 {
		return
	}
	for _, s := range requested {
		if !slices.Contains(supported, s) {
			log.Warn("oauth: requested scope is not in server's scopes_supported",
				slog.String("scope", s), slog.Any("supported", supported))
		}
	}
}
