# metabase-mcp

**Language:** **English** · [Русский](docs/README.ru.md)

Read-only MCP server on top of the Metabase REST API. Gives an LLM client (Claude Desktop, Claude Code, or any MCP-compatible host) safe access to Metabase data: list of databases, table schemas, and execution of SELECT queries.

## Features

| Tool | Purpose |
|---|---|
| `list_databases` | List of databases connected to Metabase with their `id` and `engine`. Cached for 5 minutes. |
| `list_tables` | Flat schema of a single database: tables, typed columns, foreign keys. Cached for 5 minutes per `database_id`. |
| `execute_sql` | Executes a SQL query through `/api/dataset`. Only `SELECT` and `WITH ... SELECT` are allowed. Paginates via `LIMIT`/`OFFSET` in the query; a large result is spooled to a local NDJSON file and returned as a resource link. |

### Read-only guarantees

`execute_sql` validates the query BEFORE sending it to Metabase: it parses the SQL through the TiDB AST parser and rejects anything other than a single `SELECT`/`WITH` statement. The following are blocked:

- `INSERT`, `UPDATE`, `DELETE`, `DROP`, `TRUNCATE`, `ALTER`, `CREATE`, `GRANT`, etc.;
- Multi-statement (`SELECT 1; DROP TABLE x`);
- `SELECT ... INTO OUTFILE/DUMPFILE`;
- `FOR UPDATE`, `LOCK IN SHARE MODE`;
- Comment-based bypasses like `/* SELECT */ DROP TABLE x` (the parser sees the AST, not the raw string).

### Pagination

**Pagination lives in the SQL itself.** There are no `limit`/`offset` tool arguments: pass `LIMIT` and `OFFSET` in the query and walk pages by raising `OFFSET`.

If the query has no top-level `LIMIT`, the server appends `LIMIT <row_limit> OFFSET 0`, returns the executed SQL in `meta.effective_sql` and adds a note to `meta.warnings`. `meta.next_offset` points at the offset of the next page (a hint — check `meta.row_count` to see whether the page was full). Set `EXECUTE_SQL_AUTO_LIMIT=false` to disable the rewrite; you then only get the warning.

The clause is **inserted** at the end of the statement body, not appended to the string, so a trailing `;` or comment survives: `SELECT 1; -- note` becomes `SELECT 1 LIMIT 1000 OFFSET 0; -- note`. A top-level `LIMIT` that is already there is never touched.

`row_limit` is a different mechanism and both stay:

| | `row_limit` | `LIMIT`/`OFFSET` in the SQL |
|---|---|---|
| Where it goes | `constraints.max-results` | text of the native query |
| Who applies it | Metabase, **after** the query ran on the database | the database itself |
| Role | safety cap against OOM, and the value used when the server has to append a `LIMIT` | pagination, driven by the client |

When Metabase itself cuts the result short it reports `data.rows_truncated`; that surfaces as `meta.truncated`, `meta.truncated_at` and a warning, so a partial result is never silently passed off as complete.

**Known limitation:** `sqlguard` parses with TiDB's MySQL grammar, so genuinely Postgres/ClickHouse-only syntax (`::` casts, `DISTINCT ON`, `FILTER (WHERE …)`, `ARRAY[…]`) does not pass validation in the first place. Whatever does pass is MySQL-compatible and understands `LIMIT n OFFSET m`.

### Large results (result spool)

A result whose serialized rows exceed `RESULT_INLINE_MAX_BYTES` (64 KiB by default) is **not** inlined. Instead it is written to a local NDJSON file (one row object per line) and the tool returns:

- `rows: null`, `meta.delivery: "file"`;
- `resource` with `uri` (`metabase://result/<id>`), `bytes`, `row_count`, a `preview` of the first `RESULT_PREVIEW_ROWS` rows, and — on stdio, where the client is a process on the same machine — the local `path`;
- a `resource_link` content block, so an MCP host can fetch the full set via `resources/read` on that URI. The `_meta` of that resource carries the column `key`↔`name` mapping needed to untangle duplicate names from a `JOIN`.

Why it matters: the SDK sends the payload to the client **twice** (as `structuredContent` and again as `content[0].text`), and every row object repeats every column name. 50k rows would land in the model's context in full, twice. 64 KiB of dense JSON is roughly 18–20k tokens — useful when tuning the threshold.

Force either mode with the `delivery` argument: `auto` (default), `inline`, `file`. If the spool is unavailable, `execute_sql` still succeeds: it falls back to inline and says so in `meta.warnings`.

**Data at rest.** This is the one place where the server writes query results to disk. Files live in `RESULT_SPOOL_DIR` (default `$TMPDIR/metabase-mcp/results`) with mode `0600` in a `0700` directory, under random 16-hex-character names. They are removed when the TTL expires, when the total size exceeds `RESULT_SPOOL_MAX_BYTES` (oldest first), and on process shutdown; leftovers from a `kill -9` are swept on the next start. Set `RESULT_SPOOL_ENABLED=false` to keep results in memory only — `execute_sql` then always returns rows inline.

## Configuration

Passed via environment variables:

| Variable | Required | Default | Description |
|---|---|---|---|
| `METABASE_URL` | yes | — | Metabase base URL, no trailing slash. |
| `METABASE_USER` | mode | — | Metabase login. Set **together** with `METABASE_PASSWORD` → password mode. |
| `METABASE_PASSWORD` | mode | — | Metabase password. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error`. |
| `HTTP_TIMEOUT` | no | `30s` | Any `time.ParseDuration` string (`10s`, `1m`). |

Result delivery and pagination:

| Variable | Default | Description |
|---|---|---|
| `EXECUTE_SQL_AUTO_LIMIT` | `true` | Append `LIMIT <row_limit> OFFSET 0` when the query has no top-level `LIMIT`. `false` → only warn. |
| `RESULT_INLINE_MAX_BYTES` | `65536` | Size of the serialized rows above which the result goes to a file. `0` → always inline. ≈18–20k tokens per 64 KiB of dense JSON. |
| `RESULT_PREVIEW_ROWS` | `5` | How many first rows to include in `resource.preview`. `0` → no preview. |
| `RESULT_SPOOL_ENABLED` | `true` | `false` → never write results to disk; always inline. |
| `RESULT_SPOOL_DIR` | `$TMPDIR/metabase-mcp/results` | Where spooled results live. Directory `0700`, files `0600`. |
| `RESULT_SPOOL_TTL` | `1h` | How long a spooled result stays readable. Any `time.ParseDuration` string. |
| `RESULT_SPOOL_MAX_BYTES` | `268435456` (256 MiB) | Total size cap for the spool directory; oldest results are evicted first. |

**Auth mode is chosen by presence of credentials:** both `METABASE_USER` and `METABASE_PASSWORD` set → **password mode**; neither set → **OAuth mode**; exactly one set → configuration error.

OAuth-mode variables (ignored in password mode):

| Variable | Default | Description |
|---|---|---|
| `METABASE_OAUTH_RESOURCE` | `METABASE_URL` + `/api/metabase-mcp` | RFC 8707 resource identifier. Cross-checked against the server's protected-resource metadata. |
| `METABASE_OAUTH_SCOPES` | `mb:full` | Space/comma-separated scope list. |
| `METABASE_OAUTH_CLIENT_ID` | — | Pre-registered client id. Set it to **skip** Dynamic Client Registration. |
| `METABASE_OAUTH_CLIENT_SECRET` | — | Client secret → confidential client. Requires `METABASE_OAUTH_CLIENT_ID`. |
| `METABASE_OAUTH_REDIRECT_ADDR` | `127.0.0.1:0` | Loopback address for the login callback. Loopback-only; `:0` picks a free port. |
| `METABASE_OAUTH_LOGIN_TIMEOUT` | `3m` | Timeout for the interactive first-run login. |
| `METABASE_OAUTH_NONINTERACTIVE` | `false` | `true` → never open a browser or register a client; only use/refresh a saved token, else fail. |
| `METABASE_OAUTH_RESOURCE_ON_REFRESH` | `true` | Send the RFC 8707 `resource` parameter on token refresh. Safe for conformant servers; set `false` only if a server rejects it. |
| `METABASE_TOKEN_FILE` | `$XDG_CONFIG_HOME` (or `~/.config`) `/metabase-mcp/token.json` | Where the OAuth token is persisted. Note: **not** `os.UserConfigDir()` — on macOS that would be `~/Library/...`. |

Logs go to stderr. stdout is reserved for JSON-RPC.

## Authentication

### Password mode

`POST /api/session` with login/password → session id sent as `X-Metabase-Session`. This is the original behavior; nothing changes.

### OAuth mode (OAuth 2.1 Authorization Code + PKCE + refresh)

The server acts as an OAuth **client** to Metabase. The target instance must expose OAuth discovery (`/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`); the only supported grant is Authorization Code + PKCE with `refresh_token`.

**First start (interactive, once).** If there is no usable saved token, the server:

1. binds a temporary loopback redirect (`METABASE_OAUTH_REDIRECT_ADDR`),
2. registers a public client via Dynamic Client Registration (unless `METABASE_OAUTH_CLIENT_ID` is set),
3. prints the authorization URL **to stderr** and best-effort opens your browser,
4. waits for the callback, exchanges the code (PKCE), and **persists** the token to `METABASE_TOKEN_FILE` (file `0600`, directory `0700`).

Because this blocks on a browser login, run the **first** start by hand (a terminal you can see). Complete the login in the browser; the terminal shows the printed URL if the browser did not open.

**Subsequent starts (non-interactive).** The saved refresh token is used to silently obtain access tokens. A rotated refresh token (if the server issues one) is atomically written back to the token file.

**Refresh failure at runtime.** A `401 invalid_token` triggers one silent refresh + retry. If the refresh itself fails (refresh token revoked/expired), the tool call returns an explicit error — the server does **not** pop a browser mid-session. Because a saved token with a (now dead) refresh token still counts as "usable", a plain restart reloads it; to recover, **delete `METABASE_TOKEN_FILE` and restart** to log in again interactively (`METABASE_OAUTH_NONINTERACTIVE=true` fails fast instead). A `401 insufficient_scope` (or wrong audience) is **not** masked by a refresh — it surfaces as a diagnostic error.

**One process per token file.** Concurrent processes sharing one `token.json` will fight over refresh-token rotation. Give each process its own token file, or run a single instance.

## Installation and build

Requirements: Go 1.26+.

```bash
make build      # builds ./metabase-mcp
make test       # unit tests
make lint       # vet + gofmt + golangci-lint (if installed)
```

## Usage

### Claude Desktop / Claude Code

Add the server to your MCP config:

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

In the MCP config:

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

The image is built on `distroless/static-debian12:nonroot` — a static binary, no shell, running as a non-privileged user.

**OAuth mode in Docker.** The interactive first-run login does **not** work inside the container: the loopback callback (`127.0.0.1`) is not the host, and there is no browser. Either:

- keep using **password mode** in the container; or
- log in **once on the host** to produce `token.json`, then mount it into the container **read-write** (refresh rotation must write it) and rely on the saved token:

  ```bash
  docker run -i --rm \
    -e METABASE_URL=https://metabase.example.com \
    -e METABASE_TOKEN_FILE=/token/token.json \
    -e METABASE_OAUTH_NONINTERACTIVE=true \
    -v "$HOME/.config/metabase-mcp:/token:rw" \
    metabase-mcp
  ```

No ports are exposed in either mode.

## Testing

```bash
make test                # unit tests
make test-integration    # e2e: builds the binary + FakeMetabase + real stdio handshake
```

Integration tests live in `test/` under the `integration` build tag. `test/fake_metabase.go` brings up an `httptest.Server` with a minimal implementation of the required endpoints (`/api/session`, `/api/database`, `/api/database/:id/metadata`, `/api/dataset`) plus an OAuth authorization server (`/.well-known/*`, `/oauth/register`, `/oauth/authorize`, `/oauth/token`) and Bearer acceptance on the data endpoints. The OAuth e2e seeds a `token.json` and exercises the real binary's silent-refresh + Bearer path end-to-end.

## Architecture

```
main.go
 └── server (mcp.Server)
      ├── tools (list_databases, list_tables, execute_sql)
      │    │    + resource template metabase://result/{id}
      │    ├── metabase.Client  ← HTTP client for the Metabase REST API
      │    ├── sqlguard         ← TiDB AST parser: validation + LIMIT/OFFSET facts and rewrite
      │    ├── schema           ← lean DTOs for the LLM
      │    ├── spool            ← NDJSON files for large results (TTL + eviction)
      │    └── cache            ← TTL cache (5 min)
      └── transport (stdio)
```
