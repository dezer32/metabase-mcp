package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Run запускает сервер с выбранным транспортом. Блокируется до
// дисконнекта клиента или ошибки.
//
// Сейчас поддерживается только "stdio". HTTP-транспорт зарезервирован:
// когда понадобится, добавим ветку "http" с mcp.StreamableHTTPHandler.
// Хендлеры tool'ов не зависят от транспорта, никаких правок там не нужно.
func Run(ctx context.Context, srv *mcp.Server, transport string, log *slog.Logger) error {
	switch transport {
	case "", "stdio":
		log.Info("transport: stdio")
		return srv.Run(ctx, &mcp.StdioTransport{})
	case "http":
		// При реализации помнить: на HTTP клиент — не процесс на этой машине,
		// поэтому tools.Limits.ExposeLocalPath должен остаться false
		// (см. isLocalTransport в server.go), иначе ResultRef.Path будет
		// указывать на файл, которого у клиента нет.
		return errors.New("http transport not implemented")
	default:
		return fmt.Errorf("unknown transport: %q", transport)
	}
}
