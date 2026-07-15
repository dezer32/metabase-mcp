package oauth

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dezer32/metabase-mcp/internal/config"
	"github.com/dezer32/metabase-mcp/internal/logging"
	"golang.org/x/oauth2"
)

// writeMatchingRecord пишет на диск Record, согласованный с fake AS f.
// mutate позволяет подкрутить отдельные поля (expiry, issuer, refresh).
func writeMatchingRecord(t *testing.T, path string, f *fakeAS, mutate func(*Record)) {
	t.Helper()
	rec := &Record{
		Issuer:        f.URL(),
		Resource:      f.resource(),
		TokenEndpoint: f.URL() + "/oauth/token",
		ClientID:      "dcr-client",
		RedirectURI:   "http://127.0.0.1:0/callback",
		Scopes:        []string{"mb:full"},
		ClientMode:    "public",
		RefreshToken:  "refresh-0",
		AccessToken:   "seed-access",
		Expiry:        time.Now().Add(time.Hour),
	}
	if mutate != nil {
		mutate(rec)
	}
	if err := SaveRecord(path, rec); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}
}

func TestNewTokenSource_InteractiveDCR(t *testing.T) {
	f := newFakeAS()
	defer f.Close()
	simulateBrowser(t)

	cfg := oauthTestConfig(f)
	cfg.TokenFile = filepath.Join(t.TempDir(), "token.json")

	m, err := NewTokenSource(context.Background(), cfg, f.srv.Client(), logging.Discard())
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	if f.registerCalls != 1 {
		t.Errorf("registerCalls: got %d, want 1 (DCR)", f.registerCalls)
	}
	tok, gen, err := m.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken == "" {
		t.Error("empty access token")
	}
	if gen != 0 {
		t.Errorf("initial generation should be 0, got %d", gen)
	}
	// Живой access → повторный refresh не нужен: только exchange.
	if f.tokenCalls != 1 {
		t.Errorf("tokenCalls: got %d, want 1 (exchange only)", f.tokenCalls)
	}
	// Токен сохранён на диск.
	rec, err := LoadRecord(cfg.TokenFile)
	if err != nil {
		t.Fatalf("LoadRecord: %v", err)
	}
	if rec.RefreshToken != "refresh-0" || rec.ClientID != "dcr-client" || rec.ClientMode != "public" {
		t.Errorf("persisted record unexpected: %+v", rec)
	}
}

func TestNewTokenSource_LoadsLiveToken(t *testing.T) {
	f := newFakeAS()
	defer f.Close()
	noBrowser(t) // не должно понадобиться

	cfg := oauthTestConfig(f)
	cfg.TokenFile = filepath.Join(t.TempDir(), "token.json")
	writeMatchingRecord(t, cfg.TokenFile, f, func(r *Record) {
		r.AccessToken = "live-access"
		r.Expiry = time.Now().Add(time.Hour)
	})

	m, err := NewTokenSource(context.Background(), cfg, f.srv.Client(), logging.Discard())
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	tok, _, err := m.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "live-access" {
		t.Errorf("should reuse live access token, got %q", tok.AccessToken)
	}
	if f.tokenCalls != 0 {
		t.Errorf("no token endpoint call expected for live token, got %d", f.tokenCalls)
	}
	if f.registerCalls != 0 {
		t.Errorf("no DCR expected when loading a token, got %d", f.registerCalls)
	}
}

func TestNewTokenSource_SilentRefreshExpired(t *testing.T) {
	f := newFakeAS()
	defer f.Close()
	noBrowser(t)

	cfg := oauthTestConfig(f)
	cfg.TokenFile = filepath.Join(t.TempDir(), "token.json")
	writeMatchingRecord(t, cfg.TokenFile, f, func(r *Record) {
		r.AccessToken = "stale-access"
		r.Expiry = time.Now().Add(-time.Hour) // истёк
	})

	m, err := NewTokenSource(context.Background(), cfg, f.srv.Client(), logging.Discard())
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	tok, gen, err := m.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken == "stale-access" {
		t.Error("expired token should have been silently refreshed")
	}
	if gen == 0 {
		t.Error("generation should bump after refresh")
	}
	if f.tokenCalls != 1 {
		t.Errorf("expected exactly 1 refresh call, got %d", f.tokenCalls)
	}
	// refresh grant + resource (RFC 8707) должны присутствовать.
	if f.lastTokenForm["grant_type"] != "refresh_token" {
		t.Errorf("grant_type: got %q", f.lastTokenForm["grant_type"])
	}
	if f.lastTokenForm["resource"] != f.resource() {
		t.Errorf("resource on refresh: got %q, want %q", f.lastTokenForm["resource"], f.resource())
	}
}

func TestNewTokenSource_NoninteractiveNoRecord(t *testing.T) {
	f := newFakeAS()
	defer f.Close()

	cfg := oauthTestConfig(f)
	cfg.OAuthNoninteractive = true
	cfg.TokenFile = filepath.Join(t.TempDir(), "token.json") // не существует

	_, err := NewTokenSource(context.Background(), cfg, f.srv.Client(), logging.Discard())
	if err == nil {
		t.Fatal("expected error: noninteractive + no saved token")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "NONINTERACTIVE") {
		t.Errorf("error should mention NONINTERACTIVE: %v", err)
	}
}

func TestNewTokenSource_RecordMismatchIgnored(t *testing.T) {
	f := newFakeAS()
	defer f.Close()

	cfg := oauthTestConfig(f)
	cfg.OAuthNoninteractive = true // чтобы re-auth упал явной ошибкой
	cfg.TokenFile = filepath.Join(t.TempDir(), "token.json")
	// Запишем токен от ДРУГОГО issuer — он не должен подойти.
	writeMatchingRecord(t, cfg.TokenFile, f, func(r *Record) {
		r.Issuer = "https://some-other-issuer.example"
	})

	_, err := NewTokenSource(context.Background(), cfg, f.srv.Client(), logging.Discard())
	if err == nil {
		t.Fatal("expected error: mismatched record ignored + noninteractive")
	}
}

func TestManager_ForceRefreshConditional(t *testing.T) {
	f := newFakeAS()
	defer f.Close()
	noBrowser(t)

	cfg := oauthTestConfig(f)
	cfg.TokenFile = filepath.Join(t.TempDir(), "token.json")
	writeMatchingRecord(t, cfg.TokenFile, f, func(r *Record) {
		r.Expiry = time.Now().Add(time.Hour) // живой, чтобы Token() не рефрешил
	})

	m, err := NewTokenSource(context.Background(), cfg, f.srv.Client(), logging.Discard())
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	_, gen0, _ := m.Token(context.Background())
	if gen0 != 0 {
		t.Fatalf("gen0: got %d", gen0)
	}

	// Первый force-refresh с актуальным gen — рефрешит.
	if err := m.ForceRefresh(context.Background(), gen0); err != nil {
		t.Fatalf("ForceRefresh #1: %v", err)
	}
	if f.tokenCalls != 1 {
		t.Fatalf("expected 1 refresh, got %d", f.tokenCalls)
	}
	// Повторный с УСТАРЕВШИМ gen — no-op (кто-то уже обновил).
	if err := m.ForceRefresh(context.Background(), gen0); err != nil {
		t.Fatalf("ForceRefresh #2 (stale): %v", err)
	}
	if f.tokenCalls != 1 {
		t.Fatalf("stale ForceRefresh must not refresh again, got %d", f.tokenCalls)
	}
	_, gen1, _ := m.Token(context.Background())
	if gen1 != 1 {
		t.Errorf("gen after one refresh: got %d, want 1", gen1)
	}
}

// TestManager_StaleForceRefreshRace — 50 горутин с одним и тем же снимком gen
// одновременно зовут ForceRefresh. Должен произойти РОВНО один refresh.
func TestManager_StaleForceRefreshRace(t *testing.T) {
	f := newFakeAS()
	defer f.Close()
	noBrowser(t)

	cfg := oauthTestConfig(f)
	cfg.TokenFile = filepath.Join(t.TempDir(), "token.json")
	writeMatchingRecord(t, cfg.TokenFile, f, func(r *Record) {
		r.Expiry = time.Now().Add(time.Hour)
	})

	m, err := NewTokenSource(context.Background(), cfg, f.srv.Client(), logging.Discard())
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	_, gen0, _ := m.Token(context.Background())

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = m.ForceRefresh(context.Background(), gen0)
		}()
	}
	wg.Wait()

	if f.tokenCalls != 1 {
		t.Fatalf("expected exactly 1 refresh across 50 stale-gen callers, got %d", f.tokenCalls)
	}
}

func TestManager_RefreshRotation(t *testing.T) {
	f := newFakeAS()
	f.rotateRefresh = true
	defer f.Close()
	noBrowser(t)

	cfg := oauthTestConfig(f)
	cfg.TokenFile = filepath.Join(t.TempDir(), "token.json")
	writeMatchingRecord(t, cfg.TokenFile, f, func(r *Record) {
		r.Expiry = time.Now().Add(-time.Hour) // истёк → refresh на первом Token()
	})

	m, err := NewTokenSource(context.Background(), cfg, f.srv.Client(), logging.Discard())
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	if _, _, err := m.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	// Ротированный refresh должен быть атомарно дописан в файл.
	rec, err := LoadRecord(cfg.TokenFile)
	if err != nil {
		t.Fatalf("LoadRecord: %v", err)
	}
	if rec.RefreshToken != "refresh-1" {
		t.Errorf("rotated refresh not persisted: got %q, want refresh-1", rec.RefreshToken)
	}
	if rec.AccessToken == "seed-access" || rec.AccessToken == "" {
		t.Errorf("access not updated in persisted record: %q", rec.AccessToken)
	}
}

func TestBuildClientAuth(t *testing.T) {
	meta := &Meta{TokenEndpointAuthMethods: []string{"none", "client_secret_post", "client_secret_basic"}}

	t.Run("dcr public", func(t *testing.T) {
		ca, err := buildClientAuth(config.Config{}, meta, "dcr-x")
		if err != nil {
			t.Fatal(err)
		}
		if ca.ClientID != "dcr-x" || ca.Mode != "public" || ca.AuthStyle != oauth2.AuthStyleInParams {
			t.Errorf("unexpected: %+v", ca)
		}
	})

	t.Run("configured public", func(t *testing.T) {
		ca, err := buildClientAuth(config.Config{OAuthClientID: "pub"}, meta, "")
		if err != nil {
			t.Fatal(err)
		}
		if ca.ClientID != "pub" || ca.Mode != "public" || ca.ClientSecret != "" {
			t.Errorf("unexpected: %+v", ca)
		}
	})

	t.Run("confidential prefers post", func(t *testing.T) {
		ca, err := buildClientAuth(config.Config{OAuthClientID: "c", OAuthClientSecret: "s"}, meta, "")
		if err != nil {
			t.Fatal(err)
		}
		if ca.Mode != "confidential" || ca.AuthStyle != oauth2.AuthStyleInParams || ca.ClientSecret != "s" {
			t.Errorf("unexpected: %+v", ca)
		}
	})

	t.Run("confidential basic when only basic", func(t *testing.T) {
		m2 := &Meta{TokenEndpointAuthMethods: []string{"client_secret_basic"}}
		ca, err := buildClientAuth(config.Config{OAuthClientID: "c", OAuthClientSecret: "s"}, m2, "")
		if err != nil {
			t.Fatal(err)
		}
		if ca.AuthStyle != oauth2.AuthStyleInHeader {
			t.Errorf("expected basic (InHeader), got %+v", ca)
		}
	})

	t.Run("confidential unsupported method", func(t *testing.T) {
		m2 := &Meta{TokenEndpointAuthMethods: []string{"none"}}
		_, err := buildClientAuth(config.Config{OAuthClientID: "c", OAuthClientSecret: "s"}, m2, "")
		if err == nil {
			t.Fatal("expected error when neither post nor basic supported")
		}
	})
}
