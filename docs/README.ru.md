# metabase-mcp

**Язык:** [English](../README.md) · **Русский**

Read-only MCP-сервер поверх REST-API Metabase. Даёт LLM-клиенту (Claude Desktop, Claude Code, любому MCP-совместимому хосту) безопасный доступ к данным Metabase: список БД, схема таблиц и выполнение SELECT-запросов.

## Возможности

| Tool | Назначение |
|---|---|
| `list_databases` | Список подключённых к Metabase баз с их `id` и `engine`. Кэш 5 минут. |
| `list_tables` | Плоская схема одной БД: таблицы, колонки с типами, foreign keys. Кэш 5 минут на `database_id`. |
| `execute_sql` | Выполнение SQL-запроса через `/api/dataset`. Только `SELECT` и `WITH ... SELECT`. |

### Read-only гарантии

`execute_sql` валидирует запрос ДО отправки в Metabase: парсит SQL через TiDB AST-парсер и отказывает всему, кроме одного `SELECT`/`WITH` statement'а. Запрещены:

- `INSERT`, `UPDATE`, `DELETE`, `DROP`, `TRUNCATE`, `ALTER`, `CREATE`, `GRANT`, и пр.;
- Multi-statement (`SELECT 1; DROP TABLE x`);
- `SELECT ... INTO OUTFILE/DUMPFILE`;
- `FOR UPDATE`, `LOCK IN SHARE MODE`;
- Комментарий-обходчики типа `/* SELECT */ DROP TABLE x` (парсер видит AST, не строку).

## Конфигурация

Передаётся через переменные окружения:

| Переменная | Обязательно | Значение по умолчанию | Описание |
|---|---|---|---|
| `METABASE_URL` | да | — | Базовый URL Metabase, без trailing slash. |
| `METABASE_USER` | да | — | Логин Metabase. |
| `METABASE_PASSWORD` | да | — | Пароль Metabase. |
| `LOG_LEVEL` | нет | `info` | `debug`, `info`, `warn`, `error`. |
| `HTTP_TIMEOUT` | нет | `30s` | Любая строка `time.ParseDuration` (`10s`, `1m`). |

Логи пишутся в stderr. stdout зарезервирован под JSON-RPC.

## Установка и сборка

Требования: Go 1.26+.

```bash
make build      # бинарь ./metabase-mcp
make test       # юнит-тесты
make lint       # vet + gofmt + golangci-lint (если установлен)
```

## Использование

### Claude Desktop / Claude Code

Добавь в конфиг MCP-серверов:

```json
{
  "mcpServers": {
    "metabase": {
      "command": "/absolute/path/to/metabase-mcp",
      "env": {
        "METABASE_URL": "https://metabase.example.com",
        "METABASE_USER": "bot@example.com",
        "METABASE_PASSWORD": "secret"
      }
    }
  }
}
```

### Docker

```bash
docker build -t metabase-mcp .

docker run -i --rm \
  -e METABASE_URL=https://metabase.example.com \
  -e METABASE_USER=bot@example.com \
  -e METABASE_PASSWORD=secret \
  metabase-mcp
```

В MCP-конфиге:

```json
{
  "mcpServers": {
    "metabase": {
      "command": "docker",
      "args": [
        "run", "-i", "--rm",
        "-e", "METABASE_URL",
        "-e", "METABASE_USER",
        "-e", "METABASE_PASSWORD",
        "metabase-mcp"
      ],
      "env": {
        "METABASE_URL": "https://metabase.example.com",
        "METABASE_USER": "bot@example.com",
        "METABASE_PASSWORD": "secret"
      }
    }
  }
}
```

Образ собирается на `distroless/static-debian12:nonroot` — статический бинарь, без shell, запуск от непривилегированного пользователя.

## Тестирование

```bash
make test                # юнит-тесты
make test-integration    # e2e: build бинаря + FakeMetabase + реальный stdio-handshake
```

Интеграционные тесты лежат в `test/` под build-tag `integration`. `test/fake_metabase.go` поднимает `httptest.Server` с минимальной реализацией нужных эндпоинтов (`/api/session`, `/api/database`, `/api/database/:id/metadata`, `/api/dataset`).

## Архитектура

```
main.go
 └── server (mcp.Server)
      ├── tools (list_databases, list_tables, execute_sql)
      │    ├── metabase.Client  ← HTTP-клиент к Metabase REST API
      │    ├── sqlguard.Validate ← TiDB AST-парсер
      │    ├── schema           ← lean DTO для LLM
      │    └── cache            ← TTL-кэш (5 мин)
      └── transport (stdio)
```
