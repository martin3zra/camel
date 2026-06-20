# Camel

### Explicit database migrations you can actually read.

A lightweight migration CLI for teams who want migrations they write, review, and
trust — not magic that diffs your database behind your back.

---

## The problem

Most migration tools force one of two bad trades:

- **Heavy ORM frameworks** — migrations are generated from a schema diff. You
  don't really know what SQL will run until it runs, and reviewing a migration
  means reading machine output.
- **Hand-written raw SQL** — full control, but you write `CREATE TABLE` four
  times for Postgres, MySQL, SQLite, and SQL Server, and keep them in sync by
  hand.

Teams end up either trusting a black box or maintaining four dialects of the
same migration.

## What Camel does

You declare **small, explicit intentions** in YAML (or JSON). Camel turns them
into correct SQL for **Postgres, MySQL, SQLite, and SQL Server** — and only ever
runs the files you wrote.

- **Readable** — a migration is a few lines of intent, diff-friendly in PRs.
- **Portable** — one file, correct SQL per database. Switch drivers without
  rewriting migrations.
- **Explicit** — no schema diffing, no surprises. `plan` shows the exact SQL
  before anything touches the database.
- **Escape hatch included** — drop to raw SQL whenever the DSL isn't enough.

---

## A day with Camel

Meet a developer building a blog API. Here's their week.

### Monday — start the project

```bash
$ camel init
Created camel.yaml and database/
```

`camel.yaml` picks up database credentials from the project's `.env`, so there's
nothing to wire up:

```yaml
db:
  driver: "postgres"      # or mysql, sqlite, mssql
  source: "postgres://localhost:5432/blog?sslmode=disable"

migration:
  directory: "database"
  pattern: "*.yaml"
```

### Tuesday — the first table

The migration name tells Camel what to scaffold:

```bash
$ camel make create_posts_table
Created database/20260619_create_posts_table.yaml
```

They fill in the intent using Blueprint-style column syntax:

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
      status: enum:draft,pending,published default:'draft'
      published_at: timestamp nullable
      created_at: timestamp useCurrent
      updated_at: timestamp nullable

down:
  post:
    action: drop
    table: posts
```

Before running anything, they preview the exact SQL:

```bash
$ camel migrate --pretend
-- 20260619_create_posts_table.yaml
CREATE TABLE "posts" (
  "id" BIGSERIAL NOT NULL PRIMARY KEY,
  "title" VARCHAR(150) NOT NULL,
  "slug" VARCHAR(180) NOT NULL UNIQUE,
  ...
);
```

Looks right. Ship it:

```bash
$ camel migrate
Migrated 20260619_create_posts_table.yaml
```

### Wednesday — relationships, the easy way

Posts need authors. One migration creates `users` and `posts` with a foreign key —
Camel creates the tables in the right order automatically, so the foreign key
resolves even though `posts` comes first alphabetically:

```yaml
up:
  user:
    action: create
    table: users
    columns:
      id: id
      email: string:255 unique
  post:
    action: create
    table: posts
    columns:
      id: id
      author_id: bigInteger
    foreign:
      - name: posts_author_fk
        columns: [author_id]
        references_table: users
        references_columns: [id]
        on_delete: cascade
```

The same file works whether the team runs Postgres in production or SQLite in
tests — including the foreign key, which SQLite only accepts at create time.
Camel handles that automatically. Rollback also drops tables in reverse dependency
order, so SQL Server's strict FK enforcement never blocks a reset.

### Thursday — evolve the schema

Requirements change. Add a column, rename one, add an index — all in a single
`alter`:

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
```

Camel orders the operations so a rollback is always clean — constraints and
indexes come down before the columns they depend on, and go back up after.

Writing `slug: string:180 unique` in an `add_columns` block? Camel
automatically splits that into a plain `ADD COLUMN` followed by a
`CREATE UNIQUE INDEX` — because SQLite rejects `ADD COLUMN ... UNIQUE`, and
because that index is then independently droppable on every driver. Same
migration file, right SQL everywhere.

### Friday — a data migration

The `status` column needs backfilling. Drop to raw SQL:

```yaml
up:
  backfill_status:
    action: raw
    statements:
      - "UPDATE posts SET status = 'published' WHERE published_at IS NOT NULL;"
      - "UPDATE posts SET status = 'draft' WHERE published_at IS NULL;"

down:
  backfill_status:
    action: raw
    statements:
      - "UPDATE posts SET status = 'draft';"
```

Raw statements run in the same transaction as everything else and show up in
`plan` just like generated SQL.

### When something goes wrong

```bash
$ camel status
applied  20260619_create_posts_table.yaml
applied  20260620_add_authors.yaml
applied  20260621_alter_posts.yaml
pending  20260622_backfill_status.yaml

$ camel rollback          # reverse the last batch
$ camel rollback --step 2 # reverse the last 2 migrations
$ camel reset             # roll everything back
```

Every migration runs in a transaction. If a statement fails, the whole migration
rolls back and is never recorded as applied — no half-migrated databases.

---

## Why it's safe

- **Explicit only.** Camel runs the files you wrote. It never diffs your live
  database or invents migrations.
- **Preview everything.** `plan` and `--pretend` print the exact SQL without a
  connection.
- **Transactional.** Each migration is all-or-nothing.
- **Batch-aware rollback.** A `migrate` run is one unit; rollback reverses it,
  N steps, or everything.
- **FK-ordering on up and down.** Multi-table migrations are topologically sorted
  so referenced tables exist before dependents — and dropped after them on
  rollback. Works on all four drivers including SQL Server.
- **Driver quirks handled.** `ADD COLUMN ... UNIQUE` splits automatically.
  Dropping a SQL Server column with a default drops the auto-named constraint
  first. SQLite FK constraints are declared inline at create time.
- **Honest about hard limits.** Where a database genuinely can't do something
  (SQLite has no `ALTER COLUMN`), Camel returns a clear error instead of emitting
  broken SQL.

## Where it stands

The core is built and tested end to end:

- Four database dialects: Postgres, MySQL, SQLite, SQL Server
- Full grammar: `create`, `alter`, `drop`, `raw` — columns, indexes, foreign
  keys, modify/rename, multi-table migrations with dependency ordering
- Automated unit and integration test suite (live Postgres, MySQL, SQL Server)
- Commands: `init`, `config`, `make`, `plan`, `migrate`, `rollback`, `reset`, `status`

## The ask

Camel is ready for a first real project. We're looking for:

1. A team willing to adopt it on a new service and give feedback.
2. Direction on the next dialect to harden (MySQL in CI or SQL Server edge cases).
3. Priorities for v1: a `refresh` command, seeders, or schema squashing.
