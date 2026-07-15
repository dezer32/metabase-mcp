# syntax=docker/dockerfile:1.7

# ---------- build stage ----------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Сначала зависимости — слой кэшируется отдельно от кода.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 + GOOS=linux → static binary, годится для distroless/static.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/metabase-mcp .

# ---------- runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/metabase-mcp /metabase-mcp

# Слой stdio — никаких портов, всё через stdin/stdout.
#
# OAuth-режим: интерактивный вход (браузер + loopback-колбэк) внутри контейнера
# не работает (127.0.0.1 в контейнере ≠ хост, браузера нет). Логиньтесь на хосте
# и монтируйте token.json на ЗАПИСЬ (нужно для ротации refresh) либо ставьте
# METABASE_OAUTH_NONINTERACTIVE=true. Подробности — в README.
USER nonroot:nonroot
ENTRYPOINT ["/metabase-mcp"]
