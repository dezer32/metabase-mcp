# metabase-mcp

**Язык:** [English](../README.md) · **Русский**

Read-only MCP-сервер поверх REST-API Metabase. Даёт LLM-клиенту (Claude Desktop, Claude Code, любому MCP-совместимому хосту) безопасный доступ к данным Metabase: список БД, схема таблиц и выполнение SELECT-запросов.

## Возможности

| Tool | Назначение |
|---|---|
| `list_databases` | Список подключённых к Metabase баз с их `id` и `engine`. Кэш 5 минут. |
| `list_tables` | Плоская схема одной БД: таблицы, колонки с типами, foreign keys. Кэш 5 минут на `database_id`. |
| `execute_sql` | Выполнение SQL-запроса через `/api/dataset`. Только `SELECT` и `WITH ... SELECT`. Пагинация — через `LIMIT`/`OFFSET` в запросе; большой результат уходит в локальный NDJSON-файл и возвращается resource link'ом. |

### Read-only гарантии

`execute_sql` валидирует запрос ДО отправки в Metabase: парсит SQL через TiDB AST-парсер и отказывает всему, кроме одного `SELECT`/`WITH` statement'а. Запрещены:

- `INSERT`, `UPDATE`, `DELETE`, `DROP`, `TRUNCATE`, `ALTER`, `CREATE`, `GRANT`, и пр.;
- Multi-statement (`SELECT 1; DROP TABLE x`);
- `SELECT ... INTO OUTFILE/DUMPFILE`;
- `FOR UPDATE`, `LOCK IN SHARE MODE`;
- Комментарий-обходчики типа `/* SELECT */ DROP TABLE x` (парсер видит AST, не строку).

### Пагинация

**Пагинация живёт в самом SQL.** Аргументов `limit`/`offset` у tool'а нет: передавайте `LIMIT` и `OFFSET` в запросе и листайте страницы, увеличивая `OFFSET`.

Если верхнеуровневого `LIMIT` в запросе нет, сервер дописывает `LIMIT <row_limit> OFFSET 0`, возвращает исполненный SQL в `meta.effective_sql` и добавляет запись в `meta.warnings`. `meta.next_offset` указывает на offset следующей страницы (это подсказка — полнота страницы видна по `meta.row_count`). `EXECUTE_SQL_AUTO_LIMIT=false` отключает автоподстановку, остаётся только предупреждение.

Клауза **вставляется** в конец тела statement'а, а не дописывается в конец строки, поэтому хвостовой `;` или комментарий выживают: `SELECT 1; -- заметка` превращается в `SELECT 1 LIMIT 1000 OFFSET 0; -- заметка`. Уже имеющийся верхнеуровневый `LIMIT` не трогается вообще.

`row_limit` — другой механизм, и остаются оба:

| | `row_limit` | `LIMIT`/`OFFSET` в SQL |
|---|---|---|
| Куда идёт | `constraints.max-results` | текст native-запроса |
| Кто применяет | Metabase, **после** выполнения запроса на БД | сама БД |
| Роль | предохранитель от OOM и значение для автоподстановки `LIMIT` | пагинация, задаёт клиент |

Когда результат обрезал сам Metabase, он сообщает об этом в `data.rows_truncated`; наружу это выходит как `meta.truncated`, `meta.truncated_at` и предупреждение — частичный результат никогда не выдаётся за полный.

**Известное ограничение:** `sqlguard` парсит MySQL-грамматикой TiDB, поэтому настоящий Postgres/ClickHouse-синтаксис (`::` касты, `DISTINCT ON`, `FILTER (WHERE …)`, `ARRAY[…]`) не проходит валидацию в принципе. То, что проходит, MySQL-совместимо и `LIMIT n OFFSET m` понимает.

### Большие результаты (спул)

Результат, сериализованные строки которого превышают `RESULT_INLINE_MAX_BYTES` (по умолчанию 64 KiB), инлайном **не** отдаётся. Вместо этого он пишется в локальный NDJSON-файл (по объекту-строке на строку файла), а tool возвращает:

- `rows: null`, `meta.delivery: "file"`;
- `resource` с `uri` (`metabase://result/<id>`), `bytes`, `row_count`, превью первых `RESULT_PREVIEW_ROWS` строк и — на stdio, где клиент является процессом на той же машине, — локальный `path`;
- content-блок `resource_link`, чтобы MCP-хост мог забрать полный набор через `resources/read` по этому URI. В `_meta` того ресурса лежит маппинг `key`↔`name` колонок, без которого не разобрать дубликаты имён из `JOIN`.

Зачем это нужно: SDK отправляет payload клиенту **дважды** (в `structuredContent` и ещё раз в `content[0].text`), а каждый объект-строка повторяет имя каждой колонки. 50k строк уехали бы в контекст модели целиком, два раза. 64 KiB плотного JSON — это примерно 18–20k токенов; коэффициент полезен при подборе порога.

Режим форсируется аргументом `delivery`: `auto` (по умолчанию), `inline`, `file`. Если спул недоступен, `execute_sql` всё равно успешен: откатывается к inline и пишет об этом в `meta.warnings`.

**Данные на диске.** Это единственное место, где сервер пишет результаты запросов на диск. Файлы лежат в `RESULT_SPOOL_DIR` (по умолчанию `$TMPDIR/metabase-mcp/results`) с правами `0600` в каталоге `0700`, под случайными именами из 16 hex-символов. Они удаляются по истечении TTL, при превышении `RESULT_SPOOL_MAX_BYTES` (сначала самые старые) и при остановке процесса; остатки после `kill -9` подчищаются на следующем старте. `RESULT_SPOOL_ENABLED=false` держит результаты только в памяти — тогда `execute_sql` всегда отдаёт строки инлайном.

## Конфигурация

Передаётся через переменные окружения:

| Переменная | Обязательно | Значение по умолчанию | Описание |
|---|---|---|---|
| `METABASE_URL` | да | — | Базовый URL Metabase, без trailing slash. |
| `METABASE_USER` | режим | — | Логин Metabase. Задан **вместе** с `METABASE_PASSWORD` → password-режим. |
| `METABASE_PASSWORD` | режим | — | Пароль Metabase. |
| `LOG_LEVEL` | нет | `info` | `debug`, `info`, `warn`, `error`. |
| `HTTP_TIMEOUT` | нет | `30s` | Любая строка `time.ParseDuration` (`10s`, `1m`). |

Выдача результата и пагинация:

| Переменная | По умолчанию | Описание |
|---|---|---|
| `EXECUTE_SQL_AUTO_LIMIT` | `true` | Дописывать `LIMIT <row_limit> OFFSET 0`, если верхнеуровневого `LIMIT` в запросе нет. `false` → только предупреждать. |
| `RESULT_INLINE_MAX_BYTES` | `65536` | Размер сериализованных строк, выше которого результат уходит в файл. `0` → всегда inline. ≈18–20k токенов на 64 KiB плотного JSON. |
| `RESULT_PREVIEW_ROWS` | `5` | Сколько первых строк класть в `resource.preview`. `0` → без превью. |
| `RESULT_SPOOL_ENABLED` | `true` | `false` → никогда не писать результаты на диск, всегда inline. |
| `RESULT_SPOOL_DIR` | `$TMPDIR/metabase-mcp/results` | Где лежат спуленные результаты. Каталог `0700`, файлы `0600`. |
| `RESULT_SPOOL_TTL` | `1h` | Сколько спуленный результат остаётся читаемым. Любая строка `time.ParseDuration`. |
| `RESULT_SPOOL_MAX_BYTES` | `268435456` (256 MiB) | Суммарный лимит на каталог спула; вытесняются самые старые результаты. |

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
      │    │    + шаблон ресурса metabase://result/{id}
      │    ├── metabase.Client  ← HTTP-клиент к Metabase REST API
      │    ├── sqlguard         ← TiDB AST-парсер: валидация + факты о LIMIT/OFFSET и вставка
      │    ├── schema           ← lean DTO для LLM
      │    ├── spool            ← NDJSON-файлы больших результатов (TTL + вытеснение)
      │    └── cache            ← TTL-кэш (5 мин)
      └── transport (stdio)
```
