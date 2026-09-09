package spool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dezer32/metabase-mcp/internal/logging"
)

func newStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = filepath.Join(t.TempDir(), "results")
	}
	s, err := New(cfg, logging.Discard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestEncode_NDJSON(t *testing.T) {
	rows := []map[string]any{{"a": 1}, {"a": 2}}
	b, err := Encode(rows)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("строк %d, want 2: %q", len(lines), b)
	}
	if lines[0] != `{"a":1}` || lines[1] != `{"a":2}` {
		t.Errorf("NDJSON = %q", b)
	}
	// Пустой набор — пустой файл, не "null".
	b, err = Encode(nil)
	if err != nil || len(b) != 0 {
		t.Errorf("Encode(nil) = (%q, %v)", b, err)
	}
}

func TestPutGetReadAll(t *testing.T) {
	s := newStore(t, Config{})
	data, err := Encode([]map[string]any{{"id": 1}, {"id": 2}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	e, err := s.Put(data, 2, map[string]any{"columns": []string{"id"}})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if e.RowCount != 2 || e.Bytes != int64(len(data)) {
		t.Errorf("Entry = %+v", e)
	}
	if e.URI != URIFor(e.ID) || e.MIMEType != MIMEType {
		t.Errorf("Entry URI/MIME: %+v", e)
	}

	got, ge, err := s.ReadAll(e.ID)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("ReadAll вернул %q, want %q", got, data)
	}
	if ge.ID != e.ID {
		t.Errorf("Entry из ReadAll: %+v", ge)
	}

	if _, ok := s.Get(e.ID); !ok {
		t.Error("Get: результат должен быть жив")
	}
	if _, _, err := s.ReadAll("0000000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadAll(unknown) = %v, want ErrNotFound", err)
	}
}

func TestPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "results")
	s := newStore(t, Config{Dir: dir})

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != dirPerm {
		t.Errorf("права каталога = %o, want %o", perm, dirPerm)
	}

	e, err := s.Put([]byte("{}\n"), 1, nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	fi, err = os.Stat(e.Path)
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != filePerm {
		t.Errorf("права файла = %o, want %o", perm, filePerm)
	}
}

func TestTTLExpiry(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	s := newStore(t, Config{TTL: time.Minute, Now: func() time.Time { return clock() }})

	e, err := s.Put([]byte("{}\n"), 1, nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	now = now.Add(2 * time.Minute)
	if _, ok := s.Get(e.ID); ok {
		t.Error("после TTL результат должен исчезнуть")
	}
	if _, _, err := s.ReadAll(e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadAll после TTL = %v, want ErrNotFound", err)
	}
	// Sweep обязан снести файл с диска.
	s.Sweep()
	if _, err := os.Stat(e.Path); !os.IsNotExist(err) {
		t.Errorf("файл истёкшего результата остался: %v", err)
	}
}

func TestEvictionByMaxBytes(t *testing.T) {
	s := newStore(t, Config{MaxBytes: 30})

	payload := []byte(strings.Repeat("x", 20) + "\n") // 21 байт
	first, err := s.Put(payload, 1, nil)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	second, err := s.Put(payload, 1, nil)
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	// 42 > 30 → вытеснили самый старый.
	if _, ok := s.Get(first.ID); ok {
		t.Error("первый результат должен быть вытеснен")
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Errorf("файл вытесненного результата остался: %v", err)
	}
	if _, ok := s.Get(second.ID); !ok {
		t.Error("свежий результат вытеснять нельзя")
	}
}

func TestPut_SingleOversizedResultSurvives(t *testing.T) {
	s := newStore(t, Config{MaxBytes: 1})
	e, err := s.Put([]byte(strings.Repeat("x", 100)), 1, nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := s.Get(e.ID); !ok {
		t.Error("единственный результат нельзя вытеснять из-под Put")
	}
}

func TestNew_SweepsStaleFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "results")
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	stale := filepath.Join(dir, "deadbeefdeadbeef.ndjson")
	fresh := filepath.Join(dir, "abcdef0123456789.ndjson")
	other := filepath.Join(dir, "keep.txt")
	for _, p := range []string{stale, fresh, other} {
		if err := os.WriteFile(p, []byte("{}\n"), filePerm); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	newStore(t, Config{Dir: dir, TTL: time.Hour})

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("файл старше TTL должен быть удалён: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("свежий чужой файл трогать нельзя: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("посторонний файл трогать нельзя: %v", err)
	}
}

func TestClose_RemovesOwnFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "results")
	s, err := New(Config{Dir: dir}, logging.Discard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e, err := s.Put([]byte("{}\n"), 1, nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(e.Path); !os.IsNotExist(err) {
		t.Errorf("Close должен удалить файл: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("каталог удалять нельзя: %v", err)
	}
	if _, err := s.Put([]byte("{}\n"), 1, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("Put после Close = %v, want ErrClosed", err)
	}
}

func TestPut_Concurrent(t *testing.T) {
	s := newStore(t, Config{})
	const n = 32
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := s.Put([]byte("{\"i\":1}\n"), 1, nil)
			ids[i], errs[i] = e.ID, err
		}()
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("Put %d: %v", i, errs[i])
		}
		if seen[ids[i]] {
			t.Fatalf("дубликат id %q", ids[i])
		}
		seen[ids[i]] = true
		if _, ok := s.Get(ids[i]); !ok {
			t.Errorf("результат %q потерян", ids[i])
		}
	}
}

func TestParseURI(t *testing.T) {
	id, err := ParseURI("metabase://result/0123456789abcdef")
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	if id != "0123456789abcdef" {
		t.Errorf("id = %q", id)
	}
}

func TestParseURI_Rejects(t *testing.T) {
	bad := []string{
		"",
		"metabase://result/",
		"metabase://result/../../etc/passwd",
		"metabase://result/0123456789abcdef/../x",
		"metabase://result/0123456789ABCDEF", // только lowercase hex
		"metabase://result/0123456789abcde",  // 15 символов
		"metabase://result/0123456789abcdef0",
		"metabase://result/zzzzzzzzzzzzzzzz",
		"metabase://results/0123456789abcdef",
		"file:///etc/passwd",
		"0123456789abcdef",
	}
	for _, uri := range bad {
		t.Run(uri, func(t *testing.T) {
			if _, err := ParseURI(uri); !errors.Is(err, ErrBadURI) {
				t.Errorf("ParseURI(%q) = %v, want ErrBadURI", uri, err)
			}
		})
	}
}
