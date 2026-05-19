// Package logging — обёртка над log/slog.
// КРИТИЧНО: всё пишется в stderr. На stdio-транспорте stdout — это канал
// JSON-RPC. Любой случайный вывод в stdout сломает MCP-handshake.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New строит логгер для боевого использования: TextHandler в stderr.
// level — строка из конфига ("debug"/"info"/"warn"/"error").
func New(level string) *slog.Logger {
	return newWithWriter(level, os.Stderr)
}

// Discard — логгер-заглушка для тестов, чтобы не шумел в выводе go test.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newWithWriter — внутренний конструктор, открывает writer для тестов.
func newWithWriter(level string, w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: parseLevel(level),
	}))
}

// parseLevel приводит строковый уровень к slog.Level.
// Неизвестные значения тихо мапятся на info, чтобы не падать на typo в env.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
