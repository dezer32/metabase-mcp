package oauth

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestDiscover_OK(t *testing.T) {
	f := newFakeAS()
	defer f.Close()

	meta, err := Discover(context.Background(), oauthTestConfig(f), f.srv.Client())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if meta.Issuer != f.URL() {
		t.Errorf("Issuer: got %q, want %q", meta.Issuer, f.URL())
	}
	if meta.AuthorizeEP != f.URL()+"/oauth/authorize" {
		t.Errorf("AuthorizeEP: got %q", meta.AuthorizeEP)
	}
	if meta.TokenEP != f.URL()+"/oauth/token" {
		t.Errorf("TokenEP: got %q", meta.TokenEP)
	}
	if meta.RegisterEP != f.URL()+"/oauth/register" {
		t.Errorf("RegisterEP: got %q", meta.RegisterEP)
	}
	if meta.Resource != f.resource() {
		t.Errorf("Resource: got %q, want %q", meta.Resource, f.resource())
	}
	if !slices.Contains(meta.ScopesSupported, "mb:full") {
		t.Errorf("ScopesSupported should contain mb:full: %v", meta.ScopesSupported)
	}
	if !slices.Contains(meta.TokenEndpointAuthMethods, "none") {
		t.Errorf("TokenEndpointAuthMethods should contain none: %v", meta.TokenEndpointAuthMethods)
	}
}

func TestDiscover_ResourceMismatch(t *testing.T) {
	f := newFakeAS()
	defer f.Close()

	cfg := oauthTestConfig(f)
	cfg.OAuthResource = f.URL() + "/api/WRONG"

	_, err := Discover(context.Background(), cfg, f.srv.Client())
	if err == nil {
		t.Fatal("expected resource-mismatch error")
	}
	if !strings.Contains(err.Error(), "METABASE_OAUTH_RESOURCE") {
		t.Errorf("error should mention METABASE_OAUTH_RESOURCE: %v", err)
	}
}

func TestDiscover_NoAuthServerMeta(t *testing.T) {
	f := newFakeAS()
	f.hideAuthServerMeta = true
	defer f.Close()

	_, err := Discover(context.Background(), oauthTestConfig(f), f.srv.Client())
	if err == nil {
		t.Fatal("expected error when AS metadata is absent")
	}
	if !strings.Contains(err.Error(), "authorization server metadata") {
		t.Errorf("error should mention authorization server metadata: %v", err)
	}
}

func TestDiscover_NoPRM(t *testing.T) {
	f := newFakeAS()
	f.hidePRM = true
	defer f.Close()

	_, err := Discover(context.Background(), oauthTestConfig(f), f.srv.Client())
	if err == nil {
		t.Fatal("expected error when PRM is absent")
	}
}

func TestDiscover_NoS256(t *testing.T) {
	f := newFakeAS()
	f.codeChallengeMethods = []string{"plain"}
	defer f.Close()

	_, err := Discover(context.Background(), oauthTestConfig(f), f.srv.Client())
	if err == nil {
		t.Fatal("expected error when S256 PKCE is not advertised")
	}
	if !strings.Contains(err.Error(), "S256") {
		t.Errorf("error should mention S256: %v", err)
	}
}
