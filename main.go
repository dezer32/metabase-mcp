// Command metabase-mcp — read-only MCP-сервер поверх Metabase.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dezer32/metabase-mcp/internal/config"
	"github.com/dezer32/metabase-mcp/internal/logging"
	"github.com/dezer32/metabase-mcp/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "metabase-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	transport := flag.String("transport", "stdio", "transport: stdio (http reserved)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	log := logging.New(cfg.LogLevel)
	// Глобальный slog тоже должен идти в stderr, чтобы случайные
	// slog.Info(...) из библиотек не сломали JSON-RPC канал в stdout.
	slog.SetDefault(log)

	log.Info("metabase-mcp boot",
		slog.String("metabase_url", cfg.MetabaseURL),
		slog.String("log_level", cfg.LogLevel),
		slog.Duration("http_timeout", cfg.HTTPTimeout),
		slog.String("transport", *transport),
	)

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv := server.New(cfg, log)
	if err := server.Run(ctx, srv, *transport, log); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
