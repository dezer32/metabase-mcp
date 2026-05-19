package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNew_DefaultsToInfo(t *testing.T) {
	var buf bytes.Buffer
	log := newWithWriter("", &buf) // пустой уровень → info
	log.Debug("dbg")
	log.Info("nfo")
	out := buf.String()
	if strings.Contains(out, "dbg") {
		t.Errorf("debug should be filtered at info level, got: %s", out)
	}
	if !strings.Contains(out, "nfo") {
		t.Errorf("info should appear, got: %s", out)
	}
}

func TestNew_DebugLevel(t *testing.T) {
	var buf bytes.Buffer
	log := newWithWriter("debug", &buf)
	log.Debug("dbg")
	if !strings.Contains(buf.String(), "dbg") {
		t.Errorf("debug should appear at debug level, got: %s", buf.String())
	}
}

func TestNew_UnknownFallsBackToInfo(t *testing.T) {
	var buf bytes.Buffer
	log := newWithWriter("verbose", &buf)
	log.Debug("dbg")
	if strings.Contains(buf.String(), "dbg") {
		t.Errorf("unknown level should default to info, got: %s", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"weird":   slog.LevelInfo,
	}
	for in, want := range cases {
		got := parseLevel(in)
		if got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
