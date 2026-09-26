# Database

One SQLite file. **The SQL is written so the same migrations run on PostgreSQL**,
and that is verified rather than asserted — `make check-postgres` applies this
exact migration set to a real PostgreSQL server and round-trips the types.

## The portability contract

Everything dialect-specific is confined to `internal/platform/database/`, and
every file in it that carries dialect knowledge says so at the top. The migration
bodies and the store SQL use only the DDL and DML subset both engines accept.

| Concern | The portable choice | Why not the obvious one |
|---|---|---|
| Primary key | `uuid TEXT NOT NULL PRIMARY KEY`, UUIDv7 | `INTEGER PRIMARY KEY AUTOINCREMENT` is SQLite-only; PostgreSQL wants `BIGSERIAL`/`IDENTITY`. **There is no common syntax**, so one portable schema means no integer surrogate key |
| Timestamps | `TIMESTAMPTZ`, written through `database.Time` | SQLite has no timestamp type; a bare `time.Time` is stored as Go's `time.String()` and cannot be read back. See below |
| Booleans | `BOOLEAN` with `TRUE`/`FALSE` literals | Works in both; SQLite stores 0/1 and `database/sql` converts back to `bool`. Never write `1`/`0` |
| Enums | `TEXT` + a named `CHECK (… IN (…))` | PostgreSQL `ENUM` types have no SQLite equivalent, and a CHECK is easier to migrate |
| Case-insensitive email | Store it already lower-cased, `CHECK (email = lower(email))` | `COLLATE NOCASE` is SQLite-only, `CITEXT` is a PostgreSQL extension |
| JSON | `TEXT` | `JSONB` is PostgreSQL-only. Nothing queries into `card_activity.meta`; it is read back whole. Switching later is one `ALTER … USING` |
| Upsert | `INSERT … ON CONFLICT (cols) DO UPDATE SET … excluded.x` | Supported by SQLite 3.24+ and PostgreSQL, `excluded` included |
| Getting the inserted row | `INSERT … RETURNING` | `LastInsertId()` is not supported by PostgreSQL drivers |
| Placeholders | Named parameters (`:uuid`), rebound by sqlx | `?` and `$1` differ. `:uuid` is the same text everywhere |
| `IN (…)` lists | `sqlx.In` then `Rebind` | A named parameter binds a value, not a list |
| Partial index | `CREATE INDEX … WHERE archived_at IS NULL` | Fine in both (SQLite 3.8+) |
| Timestamp defaults | **None.** Time always comes from Go | `CURRENT_TIMESTAMP` formats differently on each engine |
| Text length | `TEXT`, bounds enforced in `domain/` | `VARCHAR(n)` is accepted by both but SQLite ignores the length, so it documents a rule it does not enforce |

What is **not** portable, and is expected to be rewritten:

- `internal/platform/database/errors.go` — SQLite reports `2067` for a unique
  violation, PostgreSQL reports `23505`. The file exposes four predicates
  (`IsUniqueViolation` and friends); only their bodies change.
- `internal/platform/database/dsn.go` — pragmas are a SQLite concept.
- The two-pool arrangement in `database.go` — on PostgreSQL both fields point at
  one pool.
- `gooseDialect` in `migrate.go` — one string.

### Why UUIDv7 and not a random UUID

These values are the actual primary keys, so the index is ordered by them. A v4
key scatters every insert across the whole B-tree, turning each one into a random
page write. A v7 key leads with a millisecond timestamp, so successive inserts
land next to each other. `internal/shared/idgenerator` is the only source, and a
test asserts generated ids sort in creation order.

The cost of dropping the integer surrogate key is a 36-byte foreign key instead of
8. At a few thousand cards that is immaterial, and it removes the
`INSERT … SELECT` parent-resolution join from every store.

### The timestamp format, and why it is exactly this

```go
const Layout = "2006-01-02T15:04:05.000000Z"
```

Fixed width, always UTC, always six fractional digits. All three matter:

- **SQLite compares these columns as text.** `TIMESTAMPTZ` takes NUMERIC affinity
  and the values land as TEXT, so `ORDER BY created_at` is a lexicographic
  comparison. It is only chronological if every value is the same shape.
- **`time.RFC3339Nano` would be wrong.** It trims trailing zeros, and `.` (0x2E)
  sorts before `Z` (0x5A) — so `…:53.5Z` would sort *before* `…:53Z`. Rows would
  come back in the wrong order with no error anywhere. There is a test that
  asserts RFC3339Nano misorders, so the reason cannot be forgotten.
- **Six digits is PostgreSQL's `timestamptz` precision**, so the same strings move
  across without rounding.

`database.Time` and `database.NullTime` implement `driver.Valuer` and
`sql.Scanner`. `Scan` accepts a string, `[]byte`, **and** `time.Time` — the last
because a PostgreSQL driver returns a real `time.Time` from a `timestamptz`
column, so the models do not change when the dialect does.

## Connection

DSN pragmas, applied per connection because SQLite scopes them that way — an
`Exec` after `Open` would configure one connection out of the pool:

```
file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)
           &_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)
```

`Open` **verifies** all of these on both pools afterwards. SQLite silently ignores
a pragma it cannot parse, and a silently-off `foreign_keys` makes every
`REFERENCES` clause in this schema decoration.

### Two pools, one file

| Pool | `MaxOpenConns` | Used for |
|---|---|---|
| writer | **1** | every `INSERT`, `UPDATE`, `DELETE`, and all DDL |
| reader | `NumCPU` (or `DB_MAX_READERS`) | everything that does not write |

SQLite permits one writer at a time. Pinning the writer to a single connection
turns contention into a queue inside Go, where it is a short wait, instead of
`SQLITE_BUSY`, which never happens at a desk and reliably happens in use. A test
runs 24 concurrent transactions and asserts none fails.

Readers stay parallel under WAL. That matters because an open SSE stream holds a
read connection for as long as a board is on screen.

An in-memory database is **refused** by `Open`: the two pools would each get their
own private empty database.

### Transactions

`TxManager` carries the transaction on the context, so a service composes several
store calls into one unit of work and no store method takes a transaction
parameter.

- `Writer(ctx)` / `Reader(ctx)` return the transaction when one is active.
- **`Reader(ctx)` returning the transaction is deliberate.** The read pool is a
  different connection and cannot see uncommitted rows; a store that read around
  its own transaction would see stale data. There is a test for exactly this.
- A nested `InTx` **joins** the transaction in flight. SQLite has no nested
  transactions, and a second `Begin` on the one-connection writer pool would
  deadlock waiting for itself.

## Conventions

- **Identity.** `uuid TEXT NOT NULL PRIMARY KEY`, a UUIDv7 from
  `idgenerator.NewUUIDv7()`. Foreign keys reference `uuid` directly. There is no
  internal integer id.
- **Absence.** A store returns `(nil, nil)` for "not found", never a sentinel.
  `sql.ErrNoRows` never escapes a store — `database.GetNamed` converts it to
  `found=false`.
- **A statement that changed nothing is an error.** Every mutating store method
  runs its row count through `database.RequireRow`, which returns not-found. An
  `UPDATE` matching no rows means the row went away under the caller, and calling
  that success loses the write with no trace.
- **Scoping is authorisation.** Mutations that act on a project's contents carry
  `AND project_uuid = :project_uuid`. A card id from another board then matches
  nothing and the row count turns that into a refusal. This is why a member of one
  project cannot reorder another's cards by naming their ids.
- **Every CHECK is named** (`CONSTRAINT users_role_check CHECK …`). SQLite reports
  the constraint name for a named CHECK and the whole expression for an anonymous
  one, so naming them is what makes `database.Constraint(err)` useful.
- **Timestamps come from Go**, never from a column default.

## Tables

Eleven tables, five migrations. Every column below is `NOT NULL` unless marked
nullable.

### `users` — `00001`

| Column | Type | Notes |
|---|---|---|
| `uuid` | TEXT PK | UUIDv7 |
| `email` | TEXT | unique; stored lower-cased, enforced by CHECK |
| `display_name` | TEXT | non-empty |
| `password_hash` | TEXT | Argon2id, PHC-encoded |
| `role` | TEXT | `super_admin` \| `member` |
| `status` | TEXT | `active` \| `suspended`, default `active` |
| `must_change_password` | BOOLEAN | default `FALSE` |
| `created_at` `updated_at` | TIMESTAMPTZ | |

Indexes: `users_email_key` unique on `(email)`.

### `sessions` — `00001`

| Column | Type | Notes |
|---|---|---|
| `uuid` | TEXT PK | |
| `user_uuid` | TEXT → `users` | `ON DELETE CASCADE` |
| `token_hash` | TEXT | unique. **SHA-256 of the cookie value**; the raw token is never stored |
| `csrf_token` | TEXT | per session |
| `ip` `user_agent` | TEXT | default `''` |
| `created_at` `last_used_at` `expires_at` | TIMESTAMPTZ | |
| `revoked_at` | TIMESTAMPTZ | nullable |

Indexes: unique on `(token_hash)`; `(user_uuid)`; `(expires_at)`.

A revoked row is kept rather than deleted, so "you were signed out" can still be
explained. `DeleteExpiredSessions` prunes by `expires_at`.

### `projects` — `00002`

| Column | Type | Notes |
|---|---|---|
| `uuid` | TEXT PK | |
| `slug` | TEXT | unique; lower-case, 1–40, enforced by CHECK |
| `name` | TEXT | non-empty |
| `description` | TEXT | default `''` |
| `created_by` | TEXT → `users` | `ON DELETE RESTRICT` |
| `created_at` `updated_at` | TIMESTAMPTZ | |
| `archived_at` | TIMESTAMPTZ | nullable |

Indexes: unique on `(slug)`; `(archived_at)`.

### `project_members` — `00002`

The grant table. A row is the whole of "this person can see this project"; absence
is denial, so there is no negative grant.

| Column | Type | Notes |
|---|---|---|
| `project_uuid` | TEXT → `projects` | `ON DELETE CASCADE`, part of PK |
| `user_uuid` | TEXT → `users` | `ON DELETE CASCADE`, part of PK |
| `role` | TEXT | `manager` \| `member` |
| `granted_by` | TEXT → `users` | `ON DELETE RESTRICT` |
| `granted_at` | TIMESTAMPTZ | |

Primary key `(project_uuid, user_uuid)`. Index on `(user_uuid)`.

Written with the portable upsert, because re-granting to change a role is the
common case.

### `lanes` — `00003`

| Column | Type | Notes |
|---|---|---|
| `uuid` | TEXT PK | |
| `project_uuid` | TEXT → `projects` | `ON DELETE CASCADE` |
| `name` | TEXT | non-empty |
| `position` | INTEGER | dense, 0-based, `CHECK >= 0` |
| `color` | TEXT | default `slate`; CHECK against the palette |
| `wip_limit` | INTEGER | nullable; `CHECK (IS NULL OR > 0)` |
| `is_done` | BOOLEAN | default `FALSE` |
| `created_at` `updated_at` | TIMESTAMPTZ | |

Index: `(project_uuid, position)` — **deliberately not unique.** A reorder
rewrites every position in a loop, and a unique constraint would fire on an
intermediate state part-way through the transaction.

The colour is a closed set because it has to match a Tailwind class that exists at
build time; a free-form hex would render an unstyled lane with no error anywhere.

### `cards` — `00003`

| Column | Type | Notes |
|---|---|---|
| `uuid` | TEXT PK | |
| `project_uuid` | TEXT → `projects` | `ON DELETE CASCADE`. **Denormalised from the lane** |
| `lane_uuid` | TEXT → `lanes` | `ON DELETE RESTRICT` |
| `title` | TEXT | non-empty |
| `description` | TEXT | markdown, default `''` |
| `position` | INTEGER | dense, 0-based, `CHECK >= 0` |
| `priority` | TEXT | `none` \| `low` \| `normal` \| `high` \| `urgent` |
| `assignee_uuid` | TEXT → `users` | nullable, `ON DELETE SET NULL` |
| `due_at` | TIMESTAMPTZ | nullable |
| `created_by` | TEXT → `users` | `ON DELETE RESTRICT` |
| `created_at` `updated_at` | TIMESTAMPTZ | |
| `archived_at` | TIMESTAMPTZ | nullable |

Indexes: `(lane_uuid, position)`; `(assignee_uuid)`; `(project_uuid) WHERE
archived_at IS NULL`.

`position` is dense and zero-based per lane, and stays that way through **every**
operation: a move renumbers both affected lanes, and a delete or an archive runs
`CompactLane` in the same transaction. An invariant that held except after a removal
would be one somebody eventually relies on the wrong half of.

`project_uuid` is denormalised so every authorisation check is one lookup rather
than a join through `lanes`. `lane_uuid` is `RESTRICT` on purpose: a lane holding
cards cannot be dropped, so `DeleteLane` has to be told where they go.

### `labels`, `card_labels` — `00004`

`labels`: `uuid` PK, `project_uuid` → `projects` cascade, `name` non-empty,
`color` from the palette, `created_at`. Unique on `(project_uuid, name)`, so two
projects may both have a "bug" without sharing one.

`card_labels`: `(card_uuid, label_uuid)` composite PK, both cascading. Index on
`(label_uuid)`.

`SetLabels` replaces the whole set inside a transaction and checks in the
`INSERT … SELECT` that the label belongs to the card's project, so a label from
another board inserts nothing and the row count refuses it.

### `comments` — `00004`

`uuid` PK, `card_uuid` → `cards` cascade, `author_uuid` → `users` restrict, `body`
non-empty, `created_at`, `updated_at`, `deleted_at` nullable. Index on
`(card_uuid, created_at)`.

Soft-deleted: a removed comment leaves a tombstone so a thread does not silently
lose its shape.

### `attachments` — `00004`

`uuid` PK, `card_uuid` → `cards` cascade, `uploaded_by` → `users` restrict,
`filename`, `stored_name` (unique), `mime_type`, `size_bytes` (`CHECK >= 0`),
`created_at`. Index on `(card_uuid)`.

`stored_name` is generated; `filename` is what the uploader called it and is only
ever echoed back in a `Content-Disposition` header.

### `card_activity` — `00005`

`uuid` PK, `card_uuid` → `cards` cascade, `project_uuid` → `projects` cascade,
`actor_uuid` → `users` restrict, `kind` (CHECK against nine values),
`from_lane_uuid` / `to_lane_uuid` nullable → `lanes` set null, `meta` TEXT default
`'{}'`, `created_at`. Indexes on `(card_uuid, created_at)` and
`(project_uuid, created_at)`.

Written inside the transaction that made the change, so history cannot disagree
with the data.

## Migrations

goose, used as a **library** with `go:embed`, not as a CLI. `migrations/embed.go`
holds the embedded filesystem; `internal/platform/database/migrate.go` runs it.

- Boot and `chonkboard migrate up` share one code path, so they cannot disagree.
- There is no goose binary in the image and no entrypoint script.
- Installing the goose CLI with SQLite support means CGO or build-tag wrangling —
  `go-chi-hex/Dockerfile` in this workspace installs it with `-tags 'postgres'`
  precisely to *exclude* the SQLite driver.
- Every migration has a real `-- +goose Down`, and a test rolls the whole set back
  and asserts no table survives.
- `ApplyMigrations(ctx, db, dialect, log)` exists so the dialect is an argument in
  exactly one place. That is what the PostgreSQL portability test uses.

```bash
make migrate-up        # apply pending
make migrate-down      # roll back one
make migrate-reset     # roll everything back
make migrate-status    # what has run
make migrate-create NAME=add_card_colours
make check-postgres    # prove the set still runs on PostgreSQL (needs docker)
```

A test also asserts the embedded set matches the on-disk directory, so a migration
added but never rebuilt cannot go unnoticed.

## Backups

`VACUUM INTO` — the only correct way to snapshot a live WAL database. Copying the
file gives a torn result, because the `.db` and its `-wal` are not consistent with
each other at an arbitrary instant. Details in `PROJECT_INFRA.md`.
