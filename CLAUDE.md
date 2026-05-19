# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Что это

Read-only MCP-сервер поверх REST-API Metabase. Регистрирует три tool'а — `list_databases`, `list_tables`, `execute_sql` — и отдаёт их клиенту по stdio (JSON-RPC). HTTP-транспорт зарезервирован в `internal/server/transport.go`, но не реализован.

## Команды

```bash
make build              # сборка бинаря metabase-mcp в корень
make test               # юнит-тесты: go test -race -count=1 ./...
make test-integration   # e2e-тесты под build-tag integration (build бинаря + FakeMetabase)
make lint               # go vet + gofmt -l + golangci-lint, если установлен
make tidy               # go mod tidy

# Один тест:
go test -race -run TestE2E_ExecuteSQL_Success -tags=integration ./test/...
go test -race -run TestValidate ./internal/sqlguard/
```

CI (`.github/workflows/ci.yml`) гоняет `make build`, `make test`, `make test-integration` и golangci-lint v2.12.2 на Go 1.26.

## Запуск локально

Требуются env-переменные (валидируются на старте в `internal/config/config.go`):

- `METABASE_URL` — без trailing slash, обязательно
- `METABASE_USER`, `METABASE_PASSWORD` — обязательно
- `LOG_LEVEL` — `debug|info|warn|error`, по умолчанию `info`
- `HTTP_TIMEOUT` — `time.ParseDuration`, по умолчанию `30s`

## Архитектурные инварианты

**stdout зарезервирован под JSON-RPC.** Любой случайный `fmt.Print*` в stdout сломает MCP-handshake. Логгер из `internal/logging` пишет только в stderr, и `main.go` делает `slog.SetDefault`, чтобы случайные `slog.Info` из библиотек тоже шли в stderr.

**Read-only enforcement — двухслойный.** Не полагайся только на Metabase: пользователь мог бы прислать `DROP TABLE` в native-query. `internal/sqlguard/guard.go` парсит SQL через TiDB AST-парсер и пропускает только один statement, и только `*ast.SelectStmt` / `*ast.SetOprStmt` (UNION/INTERSECT/EXCEPT). Запрещает `SELECT INTO OUTFILE/DUMPFILE`, `FOR UPDATE`, `LOCK IN SHARE MODE`, multi-statement, комментарий-обходчики (`/* SELECT */ DROP ...` ловится через AST). При добавлении нового запрещённого паттерна — расширяй `checkReadOnly`, не regex поверх строки.

**Metabase отвечает HTTP 200 на failed-запросы.** В `internal/metabase/dataset.go` успех определяется по `resp.Status == "completed"`. Поле `error` бывает строкой, объектом (с `message`/`cause`) или вложено в `via[0].message`; `extractMetabaseError` обходит эту цепочку. Если расширяешь Dataset, **не** ориентируйся на HTTP-код для разделения успех/ошибка.

**Сессия — lazy, thread-safe, с neg-cache.** `internal/metabase/session.go` логинится при первом `ensureSession` под `sync.Mutex` с double-check, кэширует session-id. На 401/400 фиксирует `ErrAuth` на 30 секунд (defaultNegCacheTTL), чтобы не словить rate-limit от Metabase. `invalidate(usedID)` сбрасывает кэш **только если** id совпадает — иначе можно случайно затереть свежую сессию, которую успела положить параллельная горутина. Backoff'ы для логина: `[0, 500ms, 1s, 2s]`. 401 после успешного логина приводит к одной повторной попытке с новой сессией; повторный 401 — ошибка наружу.

**Кэширование tools-слоя.** `internal/server/server.go` создаёт два TTL-кэша (`cacheTTL = 5 * time.Minute`): один на полный список databases (ключ — константа `"all"`), один на metadata per `database_id`. `internal/cache/ttl.go` — generic, lazy expiry при Get/Set. При тестах tool'ов передавай свежий кэш в `Deps`, иначе результаты разъедутся между тестами.

**JSON-формат результата execute_sql.** `internal/schema/rows.go` конвертирует `[][]any` в `[]map[string]any` и дедуплицирует имена колонок: первое вхождение — как есть, последующие получают суффикс `_2`, `_3`, ... Иначе JOIN с дубликатами имён терял бы данные в map. Оригинальные имена возвращаются отдельно в `meta.columns[].name`, а ключ в `rows[i]` — в `meta.columns[].key`.

**Schema flatten для list_tables.** `internal/schema/flatten.go` отбрасывает `fingerprint`, `dimension_options`, `visibility`, и т.д. — оставляет только то, что нужно LLM для написания SQL. FK резолвятся через `fk_target_field_id` → ищем по индексу всех полей по ID; если target не найден (FK на отсутствующую таблицу), FK молча пропускается.

## Слои зависимостей

```
main.go → config, logging, server
server.go → cache, metabase, tools
tools/*.go → metabase, schema, sqlguard, cache
metabase/*.go → (stdlib only)
schema/*.go → metabase (только типы)
sqlguard/*.go → pingcap/tidb parser
```

`tools.Deps` принимает `MetabaseClient` как интерфейс, а не `*metabase.Client` — это позволяет в тестах подменять клиент моком без поднятия `httptest.Server`. Если расширяешь tool API, добавляй методы в интерфейс `MetabaseClient` в `tools/deps.go`.

## Тестирование

- Юнит-тесты лежат рядом с кодом (`*_test.go`) и не требуют build-тегов.
- Интеграционные — в `test/` под `//go:build integration`. `test/fake_metabase.go` поднимает `httptest.Server` с минимальной реализацией нужных эндпоинтов Metabase, `test/e2e_test.go` собирает реальный бинарь и подключается к нему через `mcp.CommandTransport`.
- `internal/logging.Discard()` — логгер-заглушка для тестов, чтобы не шумел в выводе.

## Go-версия

`go.mod` требует Go 1.26.3 — современный toolchain. CI использует `setup-go@v5` с `check-latest: true`.

## golangci-lint конфигурация

`govet.enable-all` намеренно НЕ включен: он бы включил `fieldalignment`, который ради экономии 8 байт ломает логический порядок полей в JSON-DTO. Список linters — явный whitelist в `.golangci.yml`. При добавлении новой проверки — клади в whitelist, не в `enable-all`.

`(io.Closer).Close` и `(*http.Response).Body` исключены из errcheck — закрытие тела ответа после прочтения не возвращает значимых ошибок.

## Стиль

Комментарии в коде — на русском, документация (godoc) — на русском. Имена пакетов/идентификаторов — на английском. При правке/добавлении кода держись того же стиля.
