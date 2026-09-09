// Package spool пишет большие результаты execute_sql в локальные
// NDJSON-файлы и отдаёт их по короткому непредсказуемому id.
//
// Смысл — контекст модели: 50k строк, отправленные инлайном, go-sdk
// продублирует ещё и в content[0].text. Вместо этого tool возвращает
// resource link + короткое превью, а полный набор лежит файлом под TTL.
//
// Пакет знает только про []byte и файловую систему; про MCP и Metabase —
// ничего.
package spool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Дефолты Config.
const (
	defaultTTL      = time.Hour
	defaultMaxBytes = 256 << 20 // 256 MiB
	fileExt         = ".ndjson"
	dirPerm         = 0o700
	filePerm        = 0o600
)

// ErrNotFound — результата с таким id нет: истёк, вытеснен или не существовал.
// Для клиента эти случаи неразличимы намеренно.
var ErrNotFound = errors.New("spool: result not found")

// ErrClosed — Store уже закрыт.
var ErrClosed = errors.New("spool: store is closed")

// Config — параметры спула. Нулевые поля заменяются дефолтами.
type Config struct {
	Dir      string           // "" → $TMPDIR/metabase-mcp/results
	TTL      time.Duration    // 0 → 1h
	MaxBytes int64            // суммарный лимит; 0 → 256 MiB
	Now      func() time.Time // подмена в тестах; nil → time.Now
}

// Entry — метаданные одного спуленного результата.
//
// Meta — произвольный JSON-объект, который вызывающий кладёт рядом с
// файлом: спул его не интерпретирует, а tools-слой отдаёт наружу в
// _meta ресурса. Без него standalone resources/read вернул бы NDJSON
// без маппинга ключей колонок на их оригинальные имена.
type Entry struct {
	ID        string
	URI       string
	Path      string
	MIMEType  string
	Bytes     int64
	RowCount  int
	CreatedAt time.Time
	Meta      map[string]any
}

// Store — каталог со спуленными результатами.
type Store struct {
	dir      string
	ttl      time.Duration
	maxBytes int64
	now      func() time.Time
	log      *slog.Logger

	mu      sync.Mutex
	entries map[string]Entry
	order   []string // id в порядке создания — FIFO для вытеснения
	total   int64
	closed  bool
}

// New создаёт каталог спула (0700) и подчищает файлы старше TTL —
// мусор, оставшийся после kill -9. Свежие чужие файлы не трогает:
// каталог может делить другой инстанс сервера.
func New(cfg Config, log *slog.Logger) (*Store, error) {
	dir := cfg.Dir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "metabase-mcp", "results")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("spool: mkdir %s: %w", dir, err)
	}

	s := &Store{
		dir:      dir,
		ttl:      ttl,
		maxBytes: maxBytes,
		now:      now,
		log:      log,
		entries:  make(map[string]Entry),
	}
	s.sweepOrphans()
	return s, nil
}

// Dir возвращает каталог спула — нужен для логов и README-диагностики.
func (s *Store) Dir() string { return s.dir }

// Put атомарно пишет NDJSON в новый файл и регистрирует его.
// Запись идёт в <id>.tmp с последующим os.Rename, чтобы читатель никогда
// не увидел недописанный файл.
func (s *Store) Put(ndjson []byte, rowCount int, meta map[string]any) (Entry, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Entry{}, ErrClosed
	}

	id, err := newID()
	if err != nil {
		return Entry{}, err
	}
	path := filepath.Join(s.dir, id+fileExt)
	tmp := filepath.Join(s.dir, id+".tmp")

	if err := os.WriteFile(tmp, ndjson, filePerm); err != nil {
		return Entry{}, fmt.Errorf("spool: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Entry{}, fmt.Errorf("spool: rename %s: %w", tmp, err)
	}

	e := Entry{
		ID:        id,
		URI:       URIFor(id),
		Path:      path,
		MIMEType:  MIMEType,
		Bytes:     int64(len(ndjson)),
		RowCount:  rowCount,
		CreatedAt: s.now(),
		Meta:      meta,
	}

	s.mu.Lock()
	s.entries[id] = e
	s.order = append(s.order, id)
	s.total += e.Bytes
	s.mu.Unlock()

	// Фонового тикера нет: чистим на записи.
	s.Sweep()
	return e, nil
}

// Get возвращает метаданные, если результат ещё жив.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok || s.expired(e) {
		return Entry{}, false
	}
	return e, true
}

// ReadAll читает файл результата целиком. Промах, истёкший TTL и
// вытеснение дают один и тот же ErrNotFound.
func (s *Store) ReadAll(id string) ([]byte, Entry, error) {
	e, ok := s.Get(id)
	if !ok {
		return nil, Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	data, err := os.ReadFile(e.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, Entry{}, fmt.Errorf("spool: read %s: %w", e.Path, err)
	}
	return data, e, nil
}

// Sweep удаляет истёкшие записи, затем вытесняет самые старые, пока сумма
// байт не влезет в MaxBytes. Последнюю запись не вытесняет: единственный
// результат, превышающий лимит сам по себе, лучше отдать, чем удалить
// прямо из-под вызывающего Put.
func (s *Store) Sweep() {
	s.mu.Lock()
	var drop []Entry
	kept := make([]string, 0, len(s.order))
	for _, id := range s.order {
		e, ok := s.entries[id]
		if !ok {
			continue
		}
		if s.expired(e) {
			delete(s.entries, id)
			s.total -= e.Bytes
			drop = append(drop, e)
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept

	for s.total > s.maxBytes && len(s.order) > 1 {
		id := s.order[0]
		s.order = s.order[1:]
		e, ok := s.entries[id]
		if !ok {
			continue
		}
		delete(s.entries, id)
		s.total -= e.Bytes
		drop = append(drop, e)
	}
	s.mu.Unlock()

	s.removeFiles(drop, "sweep")
}

// Close удаляет файлы, созданные ЭТИМ процессом. Каталог остаётся: его
// может использовать другой инстанс.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	drop := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		drop = append(drop, e)
	}
	s.entries = make(map[string]Entry)
	s.order = nil
	s.total = 0
	s.mu.Unlock()

	s.removeFiles(drop, "close")
	return nil
}

// expired — вызывать под s.mu.
func (s *Store) expired(e Entry) bool {
	return s.now().Sub(e.CreatedAt) > s.ttl
}

// removeFiles удаляет файлы вне мьютекса: ошибку логируем, но наружу
// не выносим — спул best-effort по определению.
func (s *Store) removeFiles(entries []Entry, reason string) {
	for _, e := range entries {
		if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
			s.logWarn("spool: cannot remove result file",
				slog.String("path", e.Path),
				slog.String("reason", reason),
				slog.String("err", err.Error()))
		}
	}
}

// sweepOrphans чистит файлы старше TTL по mtime — то, что осталось от
// прошлых запусков.
func (s *Store) sweepOrphans() {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		s.logWarn("spool: cannot list spool dir",
			slog.String("dir", s.dir), slog.String("err", err.Error()))
		return
	}
	deadline := s.now().Add(-s.ttl)
	for _, de := range ents {
		if de.IsDir() {
			continue
		}
		ext := filepath.Ext(de.Name())
		if ext != fileExt && ext != ".tmp" {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().After(deadline) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, de.Name())); err != nil && !os.IsNotExist(err) {
			s.logWarn("spool: cannot remove stale file",
				slog.String("name", de.Name()), slog.String("err", err.Error()))
		}
	}
}

func (s *Store) logWarn(msg string, args ...any) {
	if s.log == nil {
		return
	}
	s.log.Warn(msg, args...)
}

// newID — 8 байт из crypto/rand в hex. Каталог может лежать в общем /tmp,
// поэтому id должен быть неугадываемым; hex безопасен как имя файла.
func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("spool: random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
