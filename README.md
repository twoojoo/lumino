# Lumino

A fast, native database IDE — an alternative to DBeaver and Beekeeper Studio, written in Go with a
GPU-rendered [Gio](https://gioui.org) UI. No Electron, no JVM.

## Databases

PostgreSQL, CockroachDB, MySQL, MariaDB, TiDB, SQLite, SQL Server, Oracle, ClickHouse, Firebird,
Cassandra / ScyllaDB and Trino — all with pure-Go drivers, all covered by the same conformance suite.

## Features

- **Connections**: saved connections with passwords in the OS keyring, SSH tunnels (password, key,
  agent; known-hosts verification), TLS (require / verify CA / verify full, client certificates),
  connection URLs, init SQL, colors, production and read-only policies.
- **Navigator**: lazy tree of catalogs, schemas, tables, views, routines and sequences, with filter,
  columns, context actions (open, DDL, generate SQL, truncate, drop).
- **SQL console**: editor with syntax highlighting per dialect, context-aware completion (tables,
  aliases, columns, keywords), find/replace, multi-statement scripts (dollar quotes, `DELIMITER`, `GO`,
  PL/SQL `/`, `SET TERM`), run statement at caret or selection, cancel, error positions, auto-commit or
  manual transactions, query history and saved queries.
- **Results**: virtualized grid streaming rows from an open cursor (fetch on scroll), multiple result
  sets, client-side sort, cell selection and copy as TSV/CSV/JSON/SQL/Markdown, value viewer (text,
  JSON, hex), export to file.
- **Table view**: server-side `WHERE` and `ORDER BY`, inline editing with insert/update/delete, review
  of the generated SQL, transactional save, structure (columns, indexes, keys, constraints, triggers)
  and DDL.
- **Assistant**: Anthropic or any OpenAI-compatible provider (Ollama included); reads the schema,
  samples rows with sensitive columns redacted, runs read-only queries, proposes SQL you insert yourself.
- Ayu themes (dark, mirage, light), desktop notifications for long queries, signed self-updates.

## Development

```sh
make db-up      # disposable databases (docker compose, 127.0.0.1:55xxx)
make run        # dev build: development databases only, settings in .dev/config
make test       # unit tests
make test-int   # conformance suite of every adapter + headless UI tests (.dev/shots)
```
