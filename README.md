# metabase-mcp

**Language:** **English** · [Русский](docs/README.ru.md)

Read-only MCP server on top of the Metabase REST API. Gives an LLM client (Claude Desktop, Claude Code, or any MCP-compatible host) safe access to Metabase data: list of databases, table schemas, and execution of SELECT queries.

## Features

| Tool | Purpose |
|---|---|
| `list_databases` | List of databases connected to Metabase with their `id` and `engine`. Cached for 5 minutes. |
| `list_tables` | Flat schema of a single database: tables, typed columns, foreign keys. Cached for 5 minutes per `database_id`. |
| `execute_sql` | Executes a SQL query through `/api/dataset`. Only `SELECT` and `WITH ... SELECT` are allowed. |

### Read-only guarantees

`execute_sql` validates the query BEFORE sending it to Metabase: it parses the SQL through the TiDB AST parser and rejects anything other than a single `SELECT`/`WITH` statement. The following are blocked:

- `INSERT`, `UPDATE`, `DELETE`, `DROP`, `TRUNCATE`, `ALTER`, `CREATE`, `GRANT`, etc.;
- Multi-statement (`SELECT 1; DROP TABLE x`);
- `SELECT ... INTO OUTFILE/DUMPFILE`;
- `FOR UPDATE`, `LOCK IN SHARE MODE`;
- Comment-based bypasses like `/* SELECT */ DROP TABLE x` (the parser sees the AST, not the raw string).

## Configuration

Passed via environment variables:

| Variable | Required | Default | Description |
|---|---|---|---|
| `METABASE_URL` | yes | — | Metabase base URL, no trailing slash. |
| `METABASE_USER` | yes | — | Metabase login. |
| `METABASE_PASSWORD` | yes | — | Metabase password. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error`. |
| `HTTP_TIMEOUT` | no | `30s` | Any `time.ParseDuration` string (`10s`, `1m`). |

Logs go to stderr. stdout is reserved for JSON-RPC.

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

## Testing

```bash
make test                # unit tests
make test-integration    # e2e: builds the binary + FakeMetabase + real stdio handshake
```

Integration tests live in `test/` under the `integration` build tag. `test/fake_metabase.go` brings up an `httptest.Server` with a minimal implementation of the required endpoints (`/api/session`, `/api/database`, `/api/database/:id/metadata`, `/api/dataset`).

## Architecture

```
main.go
 └── server (mcp.Server)
      ├── tools (list_databases, list_tables, execute_sql)
      │    ├── metabase.Client  ← HTTP client for the Metabase REST API
      │    ├── sqlguard.Validate ← TiDB AST parser
      │    ├── schema           ← lean DTOs for the LLM
      │    └── cache            ← TTL cache (5 min)
      └── transport (stdio)
```
