# Camel

Explicit database migrations you can actually read.

Camel is a lightweight Go CLI for teams who want migrations they write, review, and trust — not magic that diffs your database. You declare small intentions in YAML or JSON; Camel turns them into correct SQL for **Postgres, MySQL, SQLite, and SQL Server**.

---

## Install

### Download a pre-built binary

Go to the [Releases page](https://github.com/martin3zra/camel/releases) and
download the binary for your OS:

| OS | File |
|---|---|
| macOS — Apple Silicon (M1/M2/M3) | `camel-darwin-arm64` |
| macOS — Intel | `camel-darwin-amd64` |
| Linux — x86-64 | `camel-linux-amd64` |
| Linux — ARM64 | `camel-linux-arm64` |
| Windows — x86-64 | `camel-windows-amd64.exe` |

**macOS / Linux** — make it executable and move it to your PATH:

```bash
chmod +x camel-darwin-arm64
sudo mv camel-darwin-arm64 /usr/local/bin/camel
camel init
```

**Windows** — rename the file to `camel.exe` and add its folder to your `PATH`.

A `checksums.txt` file is included in each release to verify the download.

### Install with Go (requires Go 1.22+)

```bash
go install github.com/martin3zra/camel/cmd/camel@latest
```

### Build from source

```bash
git clone https://github.com/martin3zra/camel
cd camel
go install ./cmd/camel
```

---

## Quick start

```bash
camel init                                    # create camel.yaml + database/
camel make create_posts_table                 # scaffold a YAML migration
camel make create_posts_table --format json   # scaffold a JSON migration
camel make create_my_proc --format sql        # scaffold a SQL migration (procedures, functions, triggers)
camel migrate --pretend                       # preview SQL without touching the DB
camel migrate                       # apply pending migrations
camel status                        # show applied / pending
camel rollback                      # reverse the last batch
```

---

## Migration format

Migrations are YAML (or JSON) files. The name tells Camel what to scaffold.

### Create a table

```yaml
up:
  post:
    action: create
    table: posts
    columns:
      id: id
      title: string:150
      slug: string:180 unique
      body: longText nullable
      status: enum:draft,published default:'draft'
      created_at: timestamp useCurrent
      updated_at: timestamp nullable
    indexes:
      - name: posts_slug_uq
        columns: [slug]
        unique: true

down:
  post:
    action: drop
    table: posts
```

### Alter a table

```yaml
up:
  post:
    action: alter
    table: posts
    add_columns:
      view_count: integer default:0
    rename_columns:
      title: headline
    add_indexes:
      - name: posts_headline_idx
        columns: [headline]
    add_foreign:
      - name: posts_author_fk
        columns: [author_id]
        references_table: users
        references_columns: [id]
        on_delete: cascade

down:
  post:
    action: alter
    table: posts
    drop_foreign: [posts_author_fk]
    drop_indexes: [posts_headline_idx]
    rename_columns:
      headline: title
    drop_columns: [view_count]
```

### Raw SQL (escape hatch)

```yaml
up:
  seed:
    action: raw
    statements:
      - "INSERT INTO roles (name) VALUES ('admin'), ('member');"

down:
  seed:
    action: raw
    statements:
      - "DELETE FROM roles WHERE name IN ('admin', 'member');"
```

---

## Column DSL

```text
type:attribute modifier modifier:value
```

```yaml
id: id                                    # BIGSERIAL / BIGINT AUTO_INCREMENT / INTEGER / BIGINT IDENTITY
title: string:150                         # VARCHAR(150) / NVARCHAR(150) / TEXT
slug: string:180 unique                   # unique auto-splits to ADD COLUMN + CREATE UNIQUE INDEX on all drivers
body: longText nullable                   # LONGTEXT / TEXT / NVARCHAR(MAX), allows NULL
price: decimal:8,2 default:0.00
status: enum:draft,published default:'draft'
created_at: timestamp useCurrent
```

See [DOCS.md](DOCS.md) for the full type and modifier reference, per-driver SQL output, and driver-specific behavior.

---

## Commands

| Command | Description |
|---|---|
| `camel init` | Create `camel.yaml` and the migrations directory |
| `camel config` | Print resolved configuration |
| `camel make <name> [--format yaml\|json\|sql]` | Scaffold a new migration file (YAML default) |
| `camel plan` | Print SQL for all pending migrations (alias for `migrate --pretend`) |
| `camel migrate` | Apply all pending migrations |
| `camel migrate --pretend` | Print SQL for pending migrations without executing |
| `camel rollback` | Reverse the last batch |
| `camel rollback --step N` | Reverse the last N migrations |
| `camel rollback --all` | Reverse every applied migration |
| `camel reset` | Roll everything back (alias for `rollback --all`) |
| `camel status` | List all migrations with applied / pending state |
| `camel dump` | Write current schema to `schema.sql` (human reference) |
| `camel dump --prune` | Squash applied migrations into a single SQL file |

---

## Configuration

`camel.yaml` (or `.json`) in the project root:

```yaml
db:
  driver: "postgres"   # postgres | mysql | sqlite | mssql
  source: "postgres://localhost:5432/mydb?sslmode=disable"

migration:
  directory: "database"
  pattern: "*.yaml"
  table: "camel_migrations"
```

Precedence (highest first): process env → `.env` file → `camel.yaml` → built-in defaults.

Env vars: `DB_DRIVER` / `DATABASE_DRIVER`, `DB_SOURCE` / `DATABASE_URL`.

---

## Supported drivers

| Driver | `driver` value | Notes |
|---|---|---|
| PostgreSQL | `postgres` | |
| MySQL | `mysql` | RENAME COLUMN requires MySQL 8.0+ |
| SQLite | `sqlite` | No ALTER COLUMN; FKs at create time only |
| SQL Server | `mssql` | azure-sql-edge supported on arm64 |

---

## SQL migrations (procedures, functions, triggers)

For anything the YAML DSL can't express — stored procedures, functions,
triggers, complex views — scaffold a plain `.sql` file:

```bash
camel make create_backfill_proc --format sql
```

The generated file has `-- up` and `-- down` sections. Use `GO` on its own line
as the statement separator when the body contains semicolons (required for
stored procedures):

```sql
-- up
CREATE PROCEDURE backfill_slugs()
BEGIN
  UPDATE posts SET slug = LOWER(title) WHERE slug IS NULL;
END
GO

-- down
DROP PROCEDURE IF EXISTS backfill_slugs
GO
```

`camel migrate`, `camel rollback`, and `camel status` treat `.sql` files the
same as `.yaml` files. The down section gives rollback a real inverse.

For simple DDL without internal semicolons, `GO` is optional — the parser falls
back to splitting on `;`.

> `.sql` files are driver-specific. You own dialect correctness; Camel passes
> statements through verbatim.

---

## Squashing migrations

As a project matures, the `database/` directory can accumulate hundreds of
migration files. `camel dump` condenses them:

```bash
# Write a human-readable schema.sql at the project root (no files changed).
camel dump

# Squash: delete applied migration files, replace with a single SQL file,
# keep any pending migrations untouched.
camel dump --prune
```

After `--prune` the migrations directory contains:

```
database/
├── 00000000000000_schema_dump.sql   ← full schema, runs first on a fresh DB
└── 20261220_add_something.yaml      ← any migrations applied after the dump
```

On a fresh database `camel migrate` loads `00000000000000_schema_dump.sql`
automatically — no extra commands needed. The dump file sorts before any
timestamp-prefixed migration, so the schema is always applied first.

> The generated SQL is dialect-specific. Commit `schema.sql` as a reference and
> the dump file as a migration, but note that switching drivers after a prune
> requires regenerating the dump against the new database.

---

## Why explicit migrations

- **No surprises** — Camel only runs files you wrote. It never diffs your live database.
- **Reviewable** — a migration is a few lines of intent, readable in a PR.
- **Portable** — one file produces correct SQL on all four drivers, including FK ordering and driver quirks handled automatically.
- **Transactional** — each migration is all-or-nothing. A failure rolls back and is never recorded as applied.

---

## License

MIT
