# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Camel is a lightweight database migration CLI. Users hand-write explicit migration
files (YAML or JSON) using a Laravel/Blueprint-style compact column DSL; Camel
translates each file into dialect-specific SQL and applies it. It only runs the
files you create — there is no autogeneration from a schema diff.

## Build & run

There is no committed binary. A `Makefile` wraps the common tasks (it pins
`GOCACHE` to a workspace-local dir for the sandbox); plain `go` works too:

```bash
go run ./cmd/camel <command>      # e.g. go run ./cmd/camel migrate
go build -o camel ./cmd/camel     # produce a `camel` binary
make build / make vet / make test # GOCACHE-pinned equivalents
go test ./...                     # ~84% of the camel pkg; runner exercised in-process via SQLite
go test -run TestGenerateSQL ./... # single test by name
go test -cover ./...
```

Live integration tests (`TestIntegration*` in `integration_test.go`) drive the
full create/alter/FK/index/raw + rollback cycle against a real server. They skip
unless a DSN env var is set. The Makefile spins up throwaway databases (see
`docker-compose.integration.yml`: postgres 55432, mysql 33060, mssql 14330) and
wires the DSNs:

```bash
make integration        # compose: up (all 3) -> run -> down
make integration-up     # just start the compose databases (creates the mssql `camel` db)
make integration-test   # run against already-running compose databases
make integration-down   # tear down + remove volumes
```

There is also a path for **persistent local containers** (named `mysql` :3306,
`postgres` :5433, `mssql` :1433) rather than the throwaway compose stack:

```bash
make mssql-up           # create a standalone azure-sql-edge container + camel_test db
make integration-local  # ensure camel_test dbs exist, run all 3 against the local containers
```

MS-SQL uses `mcr.microsoft.com/azure-sql-edge` (works on arm64) and needs its
database created after boot (host `sqlcmd`). Its self-signed cert gets a random
serial each boot; Go 1.24 rejects negative serials, so the MS-SQL DSN carries
`encrypt=disable&TrustServerCertificate=true` and the test run sets
`GODEBUG=x509negativeserial=1` (both wired into the Makefile). Integration
existence checks are scoped to the current database (`currentDBPredicate`) so a
same-named table in another schema on a shared server isn't a false positive.

CLI commands: `init`, `config`, `make <name> [--format yaml|json]`,
`plan [--file path] [--direction up|down]`, `migrate [--pretend]`,
`rollback [--step N] [--all] [--pretend]`, `reset [--pretend]`, `status`.
`--pretend` and `plan` print SQL without touching the DB — use them to inspect
generated SQL while iterating on dialect code.

## Module layout

The repo is a single Go module (`github.com/martin3zra/camel`). The **root
directory is the library package `camel`**; the CLI is a thin wrapper in
`cmd/camel/main.go` that only does arg parsing and printing. All real logic lives
in the root package — put new logic there, not in `cmd/`.

- `config.go` — config load/init, env + `.env` resolution
- `migration.go` — migration file model, discovery, scaffolding, up/down statement assembly
- `schema.go` — the column DSL parser and SQL generation (the core)
- `runner.go` — DB connection, transactional execution, migration-tracking table
- `main.go.bak` — dead pre-refactor monolith; ignore, do not edit

## Architecture & data flow

A command resolves config, lists/loads migration files, turns each `TableIntent`
into SQL, then (for migrate/rollback) executes inside a transaction and records
the result. Concretely:

`LoadConfig` → `ListMigrations` → per migration `StatementsFor` → per table
`GenerateSQL` (in schema.go) → `Runner.execAll`.

Key structures: a migration file is `MigrationFile{Up, Down}`, each a
`map[string]TableIntent`. The map key is a logical name; `TableIntent.Table`
overrides it (default = pluralized key). `Action` is `create` | `alter` | `drop`
| `raw`. Columns are a `map[name]definition-string`; the string is the DSL.

`raw` is the escape hatch for what the DSL can't express (data migrations, CHECK
constraints, triggers): `statements` is a list run verbatim, in order, with no
driver translation — the author owns dialect correctness. Raw statements flow
through the same transactional `execAll` and appear in `plan`/`--pretend` like
any other migration.

`create` intents take `columns` plus optional `indexes` and `foreign`. Foreign
keys are rendered as **inline table-level constraints** inside `CREATE TABLE`
(the only way to attach one in SQLite); indexes follow as separate `CREATE INDEX`
statements (portable across all four drivers).

`alter` supports add/drop/modify/rename columns plus add/drop indexes and foreign
keys (`add_columns`, `drop_columns`, `modify_columns`, `rename_columns`,
`add_indexes`, `drop_indexes`, `add_foreign`, `drop_foreign`). `alterTableSQL`
emits them in **dependency-safe order** — teardown (drop FK → drop index → drop
column) before buildup (add column → modify → rename → add index → add FK) — so a
`down` that reverses an `up` won't drop a column out from under a constraint.
SQLite rejects `modify_columns` and FK add/drop via ALTER (no `ALTER COLUMN`); the
generator returns an error rather than emit broken SQL. MS-SQL renames go through
`sp_rename`, not `RENAME COLUMN`, and dropping a column with a `default:` first
looks up and drops its auto-named DEFAULT constraint (a T-SQL batch in
`dropColumnSQL`) — Postgres/MySQL drop the default with the column. Within an
`alter`, buildup order is add → **rename → modify** → index → FK, so a `modify`
can target a column by the new name it was just renamed to in the same migration.

When one migration creates several tables, `StatementsFor` → `orderIntents`
topologically sorts them so a table is created after any table its foreign keys
reference (alphabetical otherwise; cycles fall back to alphabetical). The `down`
direction reverses this, dropping dependents first. Without this, Postgres
rejects a forward FK reference (`relation "users" does not exist`) even though
SQLite tolerates it.

### The dialect abstraction is the whole point

`schema.go` is where complexity concentrates: every SQL fragment is
driver-aware. When adding a column type, modifier, or supporting a new database,
you almost always touch these helpers together:

- `sqlType` — maps a logical type to the per-driver SQL type (switch on driver inside)
- `mapType(driver, postgres, mysql, sqlite, mssql, ...)` — the 4-dialect lookup used throughout `sqlType`
- `quoteIdent` — identifier quoting (`"` default, backticks mysql, brackets mssql)
- `placeholder` — bind params (`$N` postgres, `@pN` mssql/sqlserver, `?` others)
- `currentTimestamp`, `normalizeType`, `normalizeDriver`
- `repositorySQL` (runner.go) — the `CREATE TABLE IF NOT EXISTS` for the tracking table, also per-driver

Supported drivers: `postgres`, `mysql`, `sqlite`, `mssql`/`sqlserver`. Adding one
means extending each of the above, not just one switch.

### Column DSL

Definition string format: `type:attribute modifier modifier:value`
(e.g. `string:150`, `decimal:8,2`, `enum:draft,published default:'draft'`,
`string:180 unique nullable`). Parsed by `ParseColumn`; whitespace tokenizing is
quote-aware via `splitDefinition` (so `default:'a b'` stays one token). `id` is a
shorthand expanding to unsigned auto-increment big-integer primary key. Unknown
tokens are a hard error.

### Execution & tracking

`Runner` records applied migrations in a tracking table (default
`camel_migrations`, configurable) with a `batch` column. `migrate` applies all
pending migrations and stamps them with **one shared batch number per run**
(computed once via `nextBatch`), so a run is the rollback unit.

Rollback granularity is decided by `selectRollbackTargets` over the applied list
ordered `batch DESC, migration DESC`:
- `Rollback(RollbackOptions{})` — the most recent batch (every migration sharing
  the highest batch number)
- `RollbackOptions{Steps: N}` — the last N applied migrations regardless of batch
- `RollbackOptions{All: true}` / `Reset` — everything; `All` overrides `Steps`

Each target's `down` runs newest-first. `execAll` wraps one migration's
statements in a single transaction and rolls back on any error; the tracking row
is removed only after its `down` commits. Migrations sort lexically by filename,
so the `YYYYMMDDHHMMSS_` prefix that `make` generates defines run order.

## Config & precedence

`camel.yaml` (or `.json`) holds `db.driver`, `db.source`, and `migration.{directory,pattern,table}`.
Resolution order, highest first: process env (`DB_DRIVER`/`DATABASE_DRIVER`,
`DB_SOURCE`/`DATABASE_URL`) → `.env` file in the working dir → config file →
built-in defaults (sqlite / `camel.sqlite` / `database` / `*.yaml` / `camel_migrations`).
Format is chosen by file extension for both config and migration files. When the
migration glob ends in `.yaml`, the matching `.json` files are also picked up, so
a project can mix both formats in one directory.

## Conventions

- New behavior goes in the root `camel` package; keep `cmd/camel/main.go` as glue only.
- Map iteration is non-deterministic, so column/table output is sorted (`sortedKeys`,
  `sort.Strings`) for stable SQL — preserve that when adding generation paths.
- Errors bubble up; the CLI calls `log.Fatal`. Library functions return errors,
  they don't exit.
