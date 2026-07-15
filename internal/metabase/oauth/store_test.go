package oauth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleRecord() *Record {
	return &Record{
		Issuer:        "https://mb.example.com",
		Resource:      "https://mb.example.com/api/metabase-mcp",
		TokenEndpoint: "https://mb.example.com/oauth/token",
		ClientID:      "client-abc",
		RedirectURI:   "http://127.0.0.1:54321/callback",
		Scopes:        []string{"mb:full"},
		ClientMode:    "public",
		RefreshToken:  "refresh-xyz",
		AccessToken:   "access-xyz",
		Expiry:        time.Unix(1893456000, 0).UTC(),
	}
}

func TestStore_SaveLoadRoundTrip(t *testing.T) {
	// Каталог намеренно вложенный и несуществующий — Save обязан создать.
	path := filepath.Join(t.TempDir(), "nested", "metabase-mcp", "token.json")

	rec := sampleRecord()
	if err := SaveRecord(path, rec); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}
	got, err := LoadRecord(path)
	if err != nil {
		t.Fatalf("LoadRecord: %v", err)
	}
	if got.Issuer != rec.Issuer || got.Resource != rec.Resource ||
		got.TokenEndpoint != rec.TokenEndpoint || got.ClientID != rec.ClientID ||
		got.RedirectURI != rec.RedirectURI || got.ClientMode != rec.ClientMode ||
		got.RefreshToken != rec.RefreshToken || got.AccessToken != rec.AccessToken {
		t.Errorf("record round-trip mismatch:\n got=%+v\nwant=%+v", got, rec)
	}
	if !got.Expiry.Equal(rec.Expiry) {
		t.Errorf("Expiry: got %v, want %v", got.Expiry, rec.Expiry)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "mb:full" {
		t.Errorf("Scopes: got %v", got.Scopes)
	}
}

func TestStore_Permissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	path := filepath.Join(dir, "token.json")
	if err := SaveRecord(path, sampleRecord()); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("token file perms: got %o, want 600", got)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("token dir perms: got %o, want 700", got)
	}
}

func TestStore_SaveOverwritesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := SaveRecord(path, sampleRecord()); err != nil {
		t.Fatalf("SaveRecord #1: %v", err)
	}
	rec2 := sampleRecord()
	rec2.RefreshToken = "refresh-rotated"
	rec2.AccessToken = "access-rotated"
	if err := SaveRecord(path, rec2); err != nil {
		t.Fatalf("SaveRecord #2: %v", err)
	}
	got, err := LoadRecord(path)
	if err != nil {
		t.Fatalf("LoadRecord: %v", err)
	}
	if got.RefreshToken != "refresh-rotated" || got.AccessToken != "access-rotated" {
		t.Errorf("overwrite did not take: %+v", got)
	}
	// Не осталось temp-мусора рядом.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("expected only token.json in dir, got %d entries", len(entries))
	}
}

func TestStore_LoadFixesWidePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	if err := SaveRecord(path, sampleRecord()); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}
	// Испортим права на 0644 (читаемо всем).
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := LoadRecord(path); err != nil {
		t.Fatalf("LoadRecord: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("Load should tighten perms to 600, got %o", got)
	}
}

func TestStore_LoadMissing(t *testing.T) {
	_, err := LoadRecord(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected error loading missing file")
	}
}

func TestStore_LoadRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadRecord(dir)
	if err == nil {
		t.Fatal("expected error loading a directory as token file")
	}
}
