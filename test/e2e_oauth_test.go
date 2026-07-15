//go:build integration

package test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dezer32/metabase-mcp/internal/metabase/oauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startOAuthClient спавнит бинарь в oauth-режиме с предзасеянным token.json
// (истёкший access + валидный refresh). Бинарь при старте делает discovery,
// грузит токен (без браузера, NONINTERACTIVE=true), а на первом запросе — тихо
// рефрешит и ходит на data-эндпоинты с Bearer.
func startOAuthClient(t *testing.T, fake *FakeMetabase) *mcp.ClientSession {
	t.Helper()

	tokenFile := filepath.Join(t.TempDir(), "token.json")
	rec := &oauth.Record{
		Issuer:        fake.URL(),
		Resource:      fake.URL() + "/api/metabase-mcp",
		TokenEndpoint: fake.URL() + "/oauth/token",
		ClientID:      "e2e-client",
		RedirectURI:   "http://127.0.0.1:0/callback",
		Scopes:        []string{"mb:full"},
		ClientMode:    "public",
		RefreshToken:  "rt-seed",
		AccessToken:   "", // пусто + истёкший expiry → тихий refresh на первом Token()
		Expiry:        time.Now().Add(-time.Hour),
	}
	if err := oauth.SaveRecord(tokenFile, rec); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	binary := buildBinary(t)
	cmd := exec.Command(binary)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"METABASE_URL=" + fake.URL(),
		"METABASE_TOKEN_FILE=" + tokenFile,
		"METABASE_OAUTH_NONINTERACTIVE=true",
		"LOG_LEVEL=error",
	}

	cli := mcp.NewClient(&mcp.Implementation{Name: "e2e-oauth", Version: "0"}, nil)
	tport := &mcp.CommandTransport{Command: cmd}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	sess, err := cli.Connect(ctx, tport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func TestE2E_OAuth_ListDatabases(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()

	sess := startOAuthClient(t, fake)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_databases"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var out struct {
		Databases []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Engine string `json:"engine"`
		} `json:"databases"`
	}
	readJSON(t, res, &out)
	if len(out.Databases) != 2 {
		t.Fatalf("databases: %d", len(out.Databases))
	}
	if out.Databases[0].Name != "meta_helpdesk" {
		t.Errorf("dbs[0]: %+v", out.Databases[0])
	}
	// Истёкший access → должен был быть тихий refresh (token endpoint вызван).
	if fake.tokenCalls.Load() == 0 {
		t.Error("expected a silent refresh (token endpoint call), got none")
	}
}

func TestE2E_OAuth_ExecuteSQL(t *testing.T) {
	fake := NewFakeMetabase()
	defer fake.Close()

	sess := startOAuthClient(t, fake)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql",
		Arguments: map[string]any{
			"database_id": 3,
			"query":       "SELECT n FROM dummy",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	var out struct {
		Rows []map[string]any `json:"rows"`
		Meta struct {
			RowCount int `json:"row_count"`
		} `json:"meta"`
	}
	readJSON(t, res, &out)
	if out.Meta.RowCount != 3 {
		t.Errorf("row_count: %d", out.Meta.RowCount)
	}
}
