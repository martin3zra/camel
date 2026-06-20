# Camel column reference

How to write column definitions, every supported type and modifier, and exactly
what SQL each one produces on **Postgres, MySQL, SQLite, and SQL Server**.

The type names are inspired by [Laravel / Blueprint](https://laravel.com/docs/migrations#available-column-types),
but Camel implements a focused subset — see [Laravel parity](#laravel-parity) for
what is and isn't covered.

---

## Definition syntax

Each column is one string:

```text
type:attribute modifier modifier:value
```

- **type** — one word; may take a colon argument (`string:150`, `decimal:8,2`, `enum:a,b,c`).
- **modifiers** — space-separated words (`nullable`, `unique`, …); some take a value (`default:0`).

```yaml
columns:
  id: id
  title: string:150
  slug: string:180 unique
  amount: decimal:8,2
  status: enum:draft,published default:'draft'
  body: longText nullable
  created_at: timestamp useCurrent
```

Quoting: a default with spaces must be quoted and keeps its quotes verbatim in
the output (`default:'a b'` → `DEFAULT 'a b'`). An unknown type or modifier is a
hard error — Camel never silently guesses.

---

## Column types

Cells show the SQL Camel emits per driver. `n` = length/precision arguments.

| Camel type | Postgres | MySQL | SQLite | SQL Server | Notes |
|---|---|---|---|---|---|
| `string` *(default len 255)* | `VARCHAR(n)` | `VARCHAR(n)` | `TEXT` | `NVARCHAR(n)` | `string:150` |
| `char` *(default len 1)* | `CHAR(n)` | `CHAR(n)` | `CHAR(n)` | `NCHAR(n)` | |
| `text` | `TEXT` | `TEXT` | `TEXT` | `NVARCHAR(MAX)` | |
| `mediumText` | `TEXT` | `MEDIUMTEXT` | `TEXT` | `NVARCHAR(MAX)` | |
| `longText` | `TEXT` | `LONGTEXT` | `TEXT` | `NVARCHAR(MAX)` | |
| `integer` / `int` | `INTEGER` | `INT` | `INTEGER` | `INT` | `SERIAL` on PG if auto-increment |
| `bigInteger` / `bigint` | `BIGINT` | `BIGINT` | `INTEGER` | `BIGINT` | `BIGSERIAL` on PG if auto-increment |
| `smallInteger` / `smallint` | `SMALLINT` | `SMALLINT` | `INTEGER` | `SMALLINT` | |
| `tinyInteger` | `SMALLINT` | `TINYINT` | `INTEGER` | `TINYINT` | PG has no TINYINT |
| `boolean` / `bool` | `BOOLEAN` | `BOOLEAN` | `INTEGER` | `BIT` | |
| `decimal` *(default 8,2)* | `DECIMAL(p,s)` | `DECIMAL(p,s)` | `DECIMAL(p,s)` | `DECIMAL(p,s)` | `decimal:10,4` |
| `float` | `REAL` | `FLOAT` | `REAL` | `REAL` | |
| `double` | `DOUBLE PRECISION` | `DOUBLE` | `REAL` | `FLOAT` | |
| `date` | `DATE` | `DATE` | `DATE` | `DATE` | |
| `time` | `TIME` | `TIME` | `TIME` | `TIME` | |
| `dateTime` / `datetime` | `TIMESTAMP` | `DATETIME` | `DATETIME` | `DATETIME2` | |
| `timestamp` | `TIMESTAMP` | `TIMESTAMP` | `DATETIME` | `DATETIME2` | |
| `json` | `JSON` | `JSON` | `TEXT` | `NVARCHAR(MAX)` | SQLite stores JSON as text |
| `jsonb` | `JSONB` | `JSON` | `TEXT` | `NVARCHAR(MAX)` | only PG has real `JSONB` |
| `binary` | `BYTEA` | `BLOB` | `BLOB` | `VARBINARY(MAX)` | |
| `uuid` | `UUID` | `CHAR(36)` | `TEXT` | `UNIQUEIDENTIFIER` | only PG/MSSQL have a native type |
| `enum:a,b,c` | `TEXT` | `ENUM('a','b','c')` | `TEXT` | `TEXT` | only MySQL has native `ENUM`; others store text |

### `id` shorthand

`id` expands to a big auto-incrementing primary key. It is the portable way to
get the right idiom on each engine:

| | emitted column |
|---|---|
| Postgres | `"id" BIGSERIAL NOT NULL PRIMARY KEY` |
| MySQL | `` `id` BIGINT AUTO_INCREMENT NOT NULL PRIMARY KEY `` |
| SQLite | `"id" INTEGER NOT NULL PRIMARY KEY` *(an alias of `ROWID`)* |
| SQL Server | `[id] BIGINT IDENTITY(1,1) NOT NULL PRIMARY KEY` |

### Type arguments

| Form | Meaning |
|---|---|
| `string:150`, `char:32` | length |
| `decimal:10,4` | precision, scale |
| `enum:draft,pending,published` | allowed values (quoted into MySQL `ENUM`) |

---

## Column modifiers

| Modifier | Effect in generated SQL |
|---|---|
| *(none)* | column is `NOT NULL` by default |
| `nullable` | drops the `NOT NULL` (column allows null) |
| `unique` | appends `UNIQUE` to the column |
| `primary` / `primary_key` | appends `PRIMARY KEY` |
| `autoIncrement` / `auto_increment` | `AUTO_INCREMENT` (MySQL), `IDENTITY(1,1)` (SQL Server), or `SERIAL`/`BIGSERIAL` via the type (Postgres) |
| `unsigned` | **accepted but not emitted** — see note below |
| `default:VALUE` | `DEFAULT VALUE`, written verbatim (`default:0` → `DEFAULT 0`, `default:'draft'` → `DEFAULT 'draft'`) |
| `useCurrent` | `DEFAULT CURRENT_TIMESTAMP` (PG/MySQL/SQLite) or `DEFAULT GETDATE()` (SQL Server) |

Emission order within a column: `name type [AUTO_INCREMENT|IDENTITY] [NOT NULL]
[PRIMARY KEY] [UNIQUE] [DEFAULT ...]`.

> **`unsigned` is a no-op today.** It parses (so Laravel-style definitions don't
> error) but is not rendered into SQL. Postgres and SQLite have no `UNSIGNED`;
> MySQL does, but Camel doesn't emit it yet. Don't rely on it for constraints.

---

## Driver-specific behavior & limits

Camel is honest about what an engine can't do — it returns an error rather than
emitting SQL that will fail.

**SQLite**
- `ADD COLUMN ... UNIQUE` is handled automatically — you never need to work
  around it. Writing `slug: string:180 unique` in `add_columns` emits a plain
  `ADD COLUMN` followed by a `CREATE UNIQUE INDEX` on every driver, so migration
  files are fully portable. The auto-generated index name follows the convention
  `{table}_{column}_uq` (e.g. `posts_slug_uq`). Your `down` should drop it:
  ```yaml
  drop_indexes: [posts_slug_uq]
  drop_columns: [slug]
  ```
- `modify_columns` is unsupported (no `ALTER COLUMN`) → Camel errors with a
  suggestion to use `action: raw` with a table-rebuild pattern.
- Foreign keys can't be added or dropped via `ALTER` → declare them at create
  time with `foreign:` (see below). Camel errors clearly.
- Adding a `NOT NULL` column to a table that already has rows fails unless a
  default is given — add `nullable` or set `default:`.

**MySQL**
- Native `ENUM` and `JSON`. `RENAME COLUMN` requires MySQL 8.0+.

**SQL Server**
- Renames go through `sp_rename`, not `RENAME COLUMN`.
- Dropping a column that has a `default:` first drops its auto-named `DEFAULT`
  constraint (Camel emits the lookup automatically).
- Bind parameters are `@p1`, `@p2` (handled internally).

**Postgres**
- `SERIAL`/`BIGSERIAL` come from `integer`/`bigInteger` + auto-increment (so `id`).

---

## Indexes & foreign keys

Available on `create` (inline) and `alter`. Full examples in
[README.md](README.md).

```yaml
# create-time
foreign:
  - name: posts_author_fk
    columns: [author_id]
    references_table: users
    references_columns: [id]
    on_delete: cascade        # cascade | restrict | set null | set default | no action
    on_update: restrict
indexes:
  - name: posts_slug_uq
    columns: [slug]
    unique: true
```

```yaml
# alter
add_indexes:    [ { name: ..., columns: [...], unique: true } ]
drop_indexes:   [ index_name ]
add_foreign:    [ { name: ..., columns: [...], references_table: ..., references_columns: [...] } ]
drop_foreign:   [ constraint_name ]
```

Within one migration that creates several tables, Camel orders `CREATE TABLE`s
so a referenced table comes first (and drops them in reverse on rollback).

---

## Laravel parity

Camel borrows Laravel/Blueprint's type vocabulary but implements a deliberately
small core. Mapping and gaps:

**Supported (same names):** `id`, `string`, `char`, `text`, `mediumText`,
`longText`, `integer`, `bigInteger`, `smallInteger`, `tinyInteger`, `boolean`,
`decimal`, `float`, `double`, `date`, `time`, `dateTime`, `timestamp`, `json`,
`jsonb`, `binary`, `uuid`, `enum`.

**Supported modifiers:** `nullable`, `default`, `unique`, `primary`,
`autoIncrement`, `useCurrent` (`unsigned` parses but is not emitted).

**Not (yet) supported** — common Laravel helpers Camel does **not** have:
`foreignId`/`foreignIdFor`, `morphs`/`ulidMorphs`/`uuidMorphs`, `timestamps()`
(write `created_at`/`updated_at` explicitly), `softDeletes`, `rememberToken`,
`ulid`, `ipAddress`, `macAddress`, `year`, `set`, `geometry`/`geography`,
`unsignedBigInteger` and the other `unsigned*` integer shortcuts, and modifiers
like `after`, `comment`, `charset`, `collation`, `nullable(false)`,
`useCurrentOnUpdate`, `invisible`, `storedAs`/`virtualAs`. Composite primary keys
aren't supported (one `primary` column per table). Use `action: raw` for
anything outside the grammar.
