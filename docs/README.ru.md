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
| `METABASE_USER` | режим | — | Логин Metabase. Задан **вместе** с `METABASE_PASSWORD` → password-режим. |
| `METABASE_PASSWORD` | режим | — | Пароль Metabase. |
| `LOG_LEVEL` | нет | `info` | `debug`, `info`, `warn`, `error`. |
| `HTTP_TIMEOUT` | нет | `30s` | Любая строка `time.ParseDuration` (`10s`, `1m`). |

**Режим аутентификации выбирается по наличию учёток:** заданы и `METABASE_USER`, и `METABASE_PASSWORD` → **password-режим**; не задан ни один → **OAuth-режим**; задан ровно один → ошибка конфигурации.

Переменные OAuth-режима (в password-режиме игнорируются):

| Переменная | По умолчанию | Описание |
|---|---|---|
| `METABASE_OAUTH_RESOURCE` | `METABASE_URL` + `/api/metabase-mcp` | Идентификатор ресурса (RFC 8707). Сверяется с protected-resource metadata сервера. |
| `METABASE_OAUTH_SCOPES` | `mb:full` | Список scope через пробел/запятую. |
| `METABASE_OAUTH_CLIENT_ID` | — | Заранее зарегистрированный client id. Задать, чтобы **пропустить** Dynamic Client Registration. |
| `METABASE_OAUTH_CLIENT_SECRET` | — | Секрет → confidential-клиент. Требует `METABASE_OAUTH_CLIENT_ID`. |
| `METABASE_OAUTH_REDIRECT_ADDR` | `127.0.0.1:0` | Loopback-адрес колбэка входа. Только loopback; `:0` — свободный порт. |
| `METABASE_OAUTH_LOGIN_TIMEOUT` | `3m` | Таймаут интерактивного входа при первом старте. |
| `METABASE_OAUTH_NONINTERACTIVE` | `false` | `true` → не открывать браузер и не регистрировать клиента; только использовать/рефрешить сохранённый токен, иначе ошибка. |
| `METABASE_OAUTH_RESOURCE_ON_REFRESH` | `true` | Слать RFC 8707 `resource` на refresh. Безопасно для conformant-сервера; `false` — только если сервер его отвергает. |
| `METABASE_TOKEN_FILE` | `$XDG_CONFIG_HOME` (или `~/.config`) `/metabase-mcp/token.json` | Куда персистится OAuth-токен. Намеренно **не** `os.UserConfigDir()` — на macOS это `~/Library/...`. |

Логи пишутся в stderr. stdout зарезервирован под JSON-RPC.

## Аутентификация

### Password-режим

`POST /api/session` с логином/паролем → session-id в заголовке `X-Metabase-Session`. Исходное поведение, ничего не меняется.

### OAuth-режим (OAuth 2.1 Authorization Code + PKCE + refresh)

Сервер выступает OAuth-**клиентом** к Metabase. Целевой инстанс должен выставлять OAuth-discovery (`/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`); единственный поддерживаемый grant — Authorization Code + PKCE с `refresh_token`.

**Первый старт (интерактивно, однократно).** Если валидного сохранённого токена нет, сервер:

1. поднимает временный loopback-редирект (`METABASE_OAUTH_REDIRECT_ADDR`),
2. регистрирует публичного клиента через Dynamic Client Registration (если не задан `METABASE_OAUTH_CLIENT_ID`),
3. печатает ссылку авторизации **в stderr** и best-effort открывает браузер,
4. ждёт колбэк, меняет code на токен (PKCE) и **сохраняет** его в `METABASE_TOKEN_FILE` (файл `0600`, каталог `0700`).

Поскольку это блокируется на браузерном входе, **первый** старт выполняйте руками (в видимом терминале). Завершите вход в браузере; если браузер не открылся — в терминале напечатана ссылка.

**Последующие старты (неинтерактивно).** Сохранённый refresh-токен используется для тихого получения access-токенов. Ротированный refresh-токен (если сервер его выдаёт) атомарно дописывается в файл токена.

**Отказ refresh в рантайме.** `401 invalid_token` вызывает один тихий refresh + повтор. Если сам refresh упал (refresh-токен отозван/истёк), вызов tool'а вернёт явную ошибку — сервер **не** открывает браузер посреди сессии. Поскольку сохранённый токен с (уже мёртвым) refresh-токеном всё ещё считается «пригодным», простой перезапуск снова его загрузит; чтобы восстановиться — **удалите `METABASE_TOKEN_FILE` и перезапустите** для повторного интерактивного входа (при `METABASE_OAUTH_NONINTERACTIVE=true` — падает сразу). `401 insufficient_scope` (или неверный audience) refresh'ем **не** маскируется — возвращается диагностическая ошибка.

**Один процесс на файл токена.** Несколько процессов с общим `token.json` будут конфликтовать на ротации refresh-токена. Дайте каждому процессу свой файл токена либо держите один инстанс.

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

**OAuth-режим в Docker.** Интерактивный вход при первом старте **не** работает внутри контейнера: loopback-колбэк (`127.0.0.1`) — это не хост, и браузера нет. Варианты:

- оставить **password-режим** в контейнере; либо
- войти **один раз на хосте**, получить `token.json`, затем примонтировать его в контейнер **на запись** (ротация refresh должна писать в него) и опереться на сохранённый токен:

  ```bash
  docker run -i --rm \
    -e METABASE_URL=https://metabase.example.com \
    -e METABASE_TOKEN_FILE=/token/token.json \
    -e METABASE_OAUTH_NONINTERACTIVE=true \
    -v "$HOME/.config/metabase-mcp:/token:rw" \
    metabase-mcp
  ```

Порты не открываются ни в одном режиме.

## Тестирование

```bash
make test                # юнит-тесты
make test-integration    # e2e: build бинаря + FakeMetabase + реальный stdio-handshake
```

Интеграционные тесты лежат в `test/` под build-tag `integration`. `test/fake_metabase.go` поднимает `httptest.Server` с минимальной реализацией нужных эндпоинтов (`/api/session`, `/api/database`, `/api/database/:id/metadata`, `/api/dataset`), а также OAuth authorization server (`/.well-known/*`, `/oauth/register`, `/oauth/authorize`, `/oauth/token`) и приём Bearer на data-эндпоинтах. OAuth-e2e засевает `token.json` и прогоняет через реальный бинарь путь «тихий refresh + Bearer» целиком.

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
