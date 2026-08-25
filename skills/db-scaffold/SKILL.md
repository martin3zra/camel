---
name: db-scaffold
description: >-
  Scaffold and evolve database migrations in a project that uses camel
  (github.com/martin3zra/camel) for migrations. Converges a plain-English
  description of a data model into camel's column DSL, writes a correctly
  named create/alter migration file matching the project's existing
  conventions, and validates it with `camel plan --pretend`. Use when asked to
  create tables, add/rename/drop columns or indexes, add foreign keys, or
  otherwise design or evolve a schema in a camel project.
---

# db-scaffold

Turns "I need a posts table with an author and tags" into a correct,
camel-styled migration file — without inventing syntax camel can't parse, and
without silently adding relations or indexes the user didn't ask for.

This skill is self-contained: it does not assume camel's own source is
available, only that the target project has a `camel.yaml`/`.env` and,
ideally, the `camel` binary on `PATH` for the validation step.

## Step 1 — Learn the project before proposing anything

Read, in order:

1. **Config**: `camel.yaml` (or `.json`) in the project root. Key fields:
   `db.driver`, `migration.directory` (default `database`), `migration.pattern`
   (default `*.yaml` — note a `*.yaml` pattern also picks up `.json` files),
   `migration.table`. If no config file exists, defaults are
   `sqlite` / `database` / `*.yaml` / `camel_migrations`.
2. **Existing migration files** in `migration.directory`, sorted by filename.
   From them, learn:
   - Which tables already exist and their columns (so foreign keys reference
     real tables/columns, and you don't collide with an existing table name).
   - The project's actual style: do columns use `id` shorthand or spell out
     `bigInteger unsigned autoIncrement primary`? Do timestamps use
     `timestamp useCurrent` / `timestamp nullable`, or something else? Match
     what's already there over what this doc shows as an example.
   - Whether the project writes YAML or JSON (match the dominant format unless
     told otherwise).
3. If the directory is empty or doesn't exist yet, fall back to the DSL
   reference in Step 3 and sensible camel defaults (`id`, `created_at`/
   `updated_at` via `timestamp useCurrent` / `timestamp nullable`).

## Step 2 — Converge on the shape, in plain chat

Don't generate from a vague request. Ask ordinary conversational questions —
no special UI, this must work identically whether you're running as Claude
Code or as Cursor's agent — until you have, per table: column names, types,
nullability, uniqueness, and any foreign keys.

Guardrails while converging:

- **Build exactly what's asked.** If the request implies something you
  weren't told explicitly — a `posts` table with an `author` field that's
  probably a foreign key to `users.id`, a `slug` column that probably wants a
  unique index, a many-to-many that probably wants a pivot table — **surface
  it as a question or suggestion and wait for confirmation.** Never add it
  silently.
- **Don't invent unsupported DSL.** Composite primary keys, `morphs`,
  `softDeletes`, `foreignId` shortcuts, and similar Laravel conveniences are
  **not** part of camel's grammar (see Step 3). If the user needs one of
  these, say so plainly and offer `action: raw` with hand-written SQL as the
  escape hatch instead of faking it with the column DSL.
- **Alters need current state.** "Add a status column to posts" requires
  knowing `posts` already exists and what's on it — that's why Step 1 reads
  existing migrations first, not just the new request.

## Step 3 — Write the migration file

### Filename & location

`{directory}/{YYYYMMDDHHMMSS}_{snake_case_name}.{format}` — e.g.
`database/20260825143000_create_posts_table.yaml`. Use the current UTC time;
if a file with that exact second already exists, bump by one second. Format
is `yaml` or `json`, chosen in Step 1.

### File shape

A migration file has top-level `up` and `down` maps. Each key is a logical
table name; the value is a **table intent**:

```yaml
up:
  posts:                       # logical name; defaults to the table name
    action: create             # create | alter | drop | raw
    columns:
      id: id
      title: string:150
      slug: string:180 unique
      body: longText nullable
      author_id: bigInteger unsigned
      created_at: timestamp useCurrent
      updated_at: timestamp nullable
    indexes:
      - name: posts_slug_uq
        columns: [slug]
        unique: true
    foreign:
      - name: posts_author_fk
        columns: [author_id]
        references_table: users
        references_columns: [id]
        on_delete: cascade      # cascade | restrict | set null | set default | no action
        on_update: restrict
down:
  posts:
    action: drop
```

`alter` intents use `add_columns`, `drop_columns`, `modify_columns`,
`rename_columns` (all `map[name]` except `drop_columns` which is a list),
plus `add_indexes` / `drop_indexes` (index names) and `add_foreign` /
`drop_foreign` (constraint names). Example:

```yaml
up:
  posts:
    action: alter
    add_columns:
      status: "enum:draft,published default:'draft'"
down:
  posts:
    action: alter
    drop_columns: [status]
```

`raw` intents take a `statements` list run verbatim, no dialect translation —
use this for anything outside the DSL (CHECK constraints, triggers, data
backfills, composite PKs):

```yaml
up:
  posts:
    action: raw
    statements:
      - "ALTER TABLE posts ADD CONSTRAINT posts_price_check CHECK (price >= 0)"
down:
  posts:
    action: raw
    statements:
      - "ALTER TABLE posts DROP CONSTRAINT posts_price_check"
```

When a migration creates multiple tables, list each as its own key in `up` —
camel topologically sorts `CREATE TABLE`s by FK dependency automatically, so
declaration order in the file doesn't matter. It also reverses that order for
`down` when tables are dropped. You do not need to hand-order them, but the
`down` block should still mirror what `up` did (drop what was created,
remove what was added).

### Column DSL quick reference

Definition string: `type:attribute modifier modifier:value`. Unknown type or
modifier is a hard parse error — never guess a token that isn't listed here.

**Types** (`n` = length/precision arg where shown):

| Type | Notes |
|---|---|
| `id` | shorthand for an auto-incrementing big-integer primary key |
| `string:150` *(default 255)*, `char:32` *(default 1)* | |
| `text`, `mediumText`, `longText` | |
| `integer`/`int`, `bigInteger`/`bigint`, `smallInteger`/`smallint`, `tinyInteger` | |
| `boolean`/`bool` | |
| `decimal:8,2` *(default 8,2)*, `float`, `double` | |
| `date`, `time`, `dateTime`/`datetime`, `timestamp` | |
| `json`, `jsonb` | only Postgres has real `JSONB`; others store text/JSON |
| `binary` | |
| `uuid` | only Postgres/MSSQL have a native UUID type |
| `enum:draft,published,archived` | only MySQL has native `ENUM`; others store text |

**Modifiers**: `nullable` (default is `NOT NULL`), `unique`, `primary` /
`primary_key`, `autoIncrement` / `auto_increment`, `default:VALUE` (verbatim —
`default:0`, `default:'draft'`), `useCurrent` (→ `CURRENT_TIMESTAMP` /
`GETDATE()`). `unsigned` parses but emits nothing (no engine-portable
`UNSIGNED`; don't rely on it for constraints).

A default with spaces must be quoted and keeps its quotes:
`default:'a b'` → `DEFAULT 'a b'`.

**Not supported — do not use, fall back to `action: raw` instead**:
composite primary keys, `foreignId`/`foreignIdFor`, `morphs` family,
`timestamps()` shorthand (write `created_at`/`updated_at` explicitly),
`softDeletes`, `rememberToken`, `ulid`, `ipAddress`, `macAddress`, `year`,
`set`, `geometry`/`geography`, `unsigned*` integer shortcuts, `after`,
`comment`, `charset`, `collation`, `useCurrentOnUpdate`, `invisible`,
`storedAs`/`virtualAs`.

**SQLite-specific limits** (still write portable DSL; camel handles the
translation, but these operations aren't available at all on SQLite via
`alter`): no `modify_columns` (no `ALTER COLUMN` — use `action: raw` with a
table-rebuild), no `add_foreign`/`drop_foreign` via alter (declare FKs at
`create` time only), adding a `NOT NULL` column to a non-empty table needs
`nullable` or a `default:`.

## Step 4 — Validate before calling it done

After writing the file, run (from the project root, if `camel` is on `PATH`):

```bash
camel plan --file <path/to/migration> --direction up
camel plan --file <path/to/migration> --direction down
```

Show the generated SQL to the user. If it errors, fix the migration file and
re-run — never leave a migration file behind that fails `camel plan`. If the
`camel` binary isn't available in the target project, say so explicitly and
skip validation rather than guessing at correctness.

`camel plan` only renders SQL from the DSL — it never touches the database,
so it can't see state-dependent failures: a column that already exists, a
`NOT NULL` column added to a table that already has rows, a foreign key whose
target isn't actually there. For an `alter` against a table you know already
has data (or whenever the user wants stronger confidence before a real
`migrate`), also offer:

```bash
camel migrate --dry-run
```

This runs every pending migration for real against the project's configured
database inside a transaction that's always rolled back — nothing is
committed, nothing is recorded as applied. It refuses on MySQL (its DDL
auto-commits, so a rollback can't undo it); for MySQL projects, say so and
suggest running a real `camel migrate` against a disposable copy of the
database instead. Only suggest `--dry-run` against a database the user is
comfortable connecting to for this purpose — don't run it unprompted against
what might be a shared or production DSN.

## What this skill will not do

- Autogenerate migrations from a live database diff — camel's design is
  hand-written migrations; this skill assists writing them, it doesn't
  replace that with inference from schema state.
- Silently add foreign keys, indexes, pivot tables, or soft-delete-style
  columns beyond what was asked — always surface these as a suggestion first.
- Use DSL syntax camel doesn't support, even if a Laravel-style shorthand
  looks tempting — use `action: raw` for anything outside the grammar above.
