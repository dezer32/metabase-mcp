package oauth

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestRegister_OK(t *testing.T) {
	f := newFakeAS()
	defer f.Close()

	meta := &Meta{RegisterEP: f.URL() + "/oauth/register"}
	redirectURI := "http://127.0.0.1:12345/callback"

	resp, err := Register(context.Background(), meta, redirectURI, []string{"mb:full"}, f.srv.Client())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.ClientID != "dcr-client" {
		t.Errorf("ClientID: got %q, want dcr-client", resp.ClientID)
	}
	if f.registerCalls != 1 {
		t.Errorf("registerCalls: got %d, want 1", f.registerCalls)
	}
	if !slices.Contains(resp.RedirectURIs, redirectURI) {
		t.Errorf("redirect_uris should echo %q: %v", redirectURI, resp.RedirectURIs)
	}
}

func TestRegister_NoEndpoint(t *testing.T) {
	meta := &Meta{RegisterEP: ""}
	_, err := Register(context.Background(), meta, "http://127.0.0.1:1/cb", []string{"mb:full"}, nil)
	if err == nil {
		t.Fatal("expected error when registration_endpoint is empty")
	}
	if !strings.Contains(err.Error(), "CLIENT_ID") {
		t.Errorf("error should hint at setting CLIENT_ID: %v", err)
	}
}
