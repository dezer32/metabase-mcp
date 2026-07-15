package oauth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dezer32/metabase-mcp/internal/logging"
	"golang.org/x/oauth2"
)

// simulateBrowser подменяет browserOpener так, чтобы вместо реального браузера
// программно дёрнуть authorize-URL (следуя редиректу на loopback-колбэк).
func simulateBrowser(t *testing.T) {
	t.Helper()
	prev := browserOpener
	t.Cleanup(func() { browserOpener = prev })
	browserOpener = func(rawURL string) {
		go func() {
			resp, err := http.Get(rawURL) //nolint:bodyclose // закрываем ниже
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
}

func noBrowser(t *testing.T) {
	t.Helper()
	prev := browserOpener
	t.Cleanup(func() { browserOpener = prev })
	browserOpener = func(string) {}
}

func publicAuth() ClientAuth {
	return ClientAuth{ClientID: "client-x", Mode: "public", AuthStyle: oauth2.AuthStyleInParams}
}

func metaFor(f *fakeAS) *Meta {
	return &Meta{
		AuthorizeEP: f.URL() + "/oauth/authorize",
		TokenEP:     f.URL() + "/oauth/token",
		Resource:    f.resource(),
	}
}

func TestBindCallback_Loopback(t *testing.T) {
	lis, redirectURI, err := BindCallback("127.0.0.1:0")
	if err != nil {
		t.Fatalf("BindCallback: %v", err)
	}
	defer lis.Close()
	if !strings.HasPrefix(redirectURI, "http://127.0.0.1:") {
		t.Errorf("redirectURI: got %q, want http://127.0.0.1:PORT/...", redirectURI)
	}
}

func TestBindCallback_RejectsNonLoopback(t *testing.T) {
	_, _, err := BindCallback("0.0.0.0:0")
	if err == nil {
		t.Fatal("expected error binding to non-loopback address")
	}
}

func TestInteractiveLogin_OK(t *testing.T) {
	f := newFakeAS()
	defer f.Close()
	simulateBrowser(t)

	lis, redirectURI, err := BindCallback("127.0.0.1:0")
	if err != nil {
		t.Fatalf("BindCallback: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tok, err := InteractiveLogin(ctx, metaFor(f), publicAuth(), lis, redirectURI,
		[]string{"mb:full"}, f.resource(), logging.Discard())
	if err != nil {
		t.Fatalf("InteractiveLogin: %v", err)
	}
	if tok.AccessToken == "" {
		t.Error("empty access token")
	}
	if tok.RefreshToken != "refresh-0" {
		t.Errorf("refresh token: got %q, want refresh-0", tok.RefreshToken)
	}
	// Exchange должен был уйти с PKCE-verifier и RFC 8707 resource.
	if f.lastTokenForm["grant_type"] != "authorization_code" {
		t.Errorf("grant_type: got %q", f.lastTokenForm["grant_type"])
	}
	if f.lastTokenForm["code"] != "auth-code-123" {
		t.Errorf("code: got %q", f.lastTokenForm["code"])
	}
	if f.lastTokenForm["code_verifier"] == "" {
		t.Error("missing code_verifier in exchange")
	}
	if f.lastTokenForm["resource"] != f.resource() {
		t.Errorf("resource: got %q, want %q", f.lastTokenForm["resource"], f.resource())
	}
	// Public client: client_id в форме, секрета нет.
	if f.lastTokenForm["client_id"] != "client-x" {
		t.Errorf("client_id: got %q", f.lastTokenForm["client_id"])
	}
	if _, ok := f.lastTokenForm["client_secret"]; ok {
		t.Error("public client must not send client_secret")
	}
}

func TestInteractiveLogin_NoRefreshToken(t *testing.T) {
	f := newFakeAS()
	f.omitRefreshTok = true
	defer f.Close()
	simulateBrowser(t)

	lis, redirectURI, _ := BindCallback("127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := InteractiveLogin(ctx, metaFor(f), publicAuth(), lis, redirectURI,
		[]string{"mb:full"}, f.resource(), logging.Discard())
	if err == nil {
		t.Fatal("expected error when AS returns no refresh_token")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refresh") {
		t.Errorf("error should mention refresh token: %v", err)
	}
}

func TestInteractiveLogin_StateMismatch(t *testing.T) {
	f := newFakeAS()
	f.authStateOverride = "attacker-state"
	defer f.Close()
	simulateBrowser(t)

	lis, redirectURI, _ := BindCallback("127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := InteractiveLogin(ctx, metaFor(f), publicAuth(), lis, redirectURI,
		[]string{"mb:full"}, f.resource(), logging.Discard())
	if err == nil {
		t.Fatal("expected state-mismatch error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "state") {
		t.Errorf("error should mention state: %v", err)
	}
}

func TestInteractiveLogin_AuthorizeError(t *testing.T) {
	f := newFakeAS()
	f.authErrorParam = "access_denied"
	defer f.Close()
	simulateBrowser(t)

	lis, redirectURI, _ := BindCallback("127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := InteractiveLogin(ctx, metaFor(f), publicAuth(), lis, redirectURI,
		[]string{"mb:full"}, f.resource(), logging.Discard())
	if err == nil {
		t.Fatal("expected authorize error to surface")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("error should mention access_denied: %v", err)
	}
}

func TestInteractiveLogin_Timeout(t *testing.T) {
	f := newFakeAS()
	defer f.Close()
	noBrowser(t) // браузер не дёргаем → колбэк не придёт → таймаут

	lis, redirectURI, _ := BindCallback("127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := InteractiveLogin(ctx, metaFor(f), publicAuth(), lis, redirectURI,
		[]string{"mb:full"}, f.resource(), logging.Discard())
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
