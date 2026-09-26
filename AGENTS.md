# Chonkboard — rules for anyone (or anything) writing code here

Durable project records live in **`.project-doc/`**. Read
`.project-doc/docs/PROJECT_KNOWLEDGE.md` and
`.project-doc/memory/DECISIONS.md` before starting; update
`.project-doc/status/` as work lands.

This file wins over any code comment. Where it and a comment disagree, the
comment is stale.

## 1. What this is

A self-hosted kanban board. One Go binary, one SQLite file, no other services.
One super admin (the owner) creates projects, lanes, and user accounts. Members
create, edit, and drag cards and can change nothing about the board's shape.

## 2. Layout

```
cmd/chonkboard/        composition root + migrate/seed/backup subcommands
migrations/            goose SQL, embedded via go:embed
internal/
  auth/                login, sessions, CSRF, own password
  project/             projects + membership
  board/               lanes + labels
  card/                cards, comments, attachments, activity
  admin/               user CRUD, password reset, project grants
    <slice>/domain/    entities and rules — no framework imports
    <slice>/service/   use cases; declares the ports it needs
    <slice>/handler/   chi handlers, form decoding, rendering
    <slice>/store.go   the SQLite implementation of this slice's ports
  platform/            config, sqlite, httpserver, middleware, backup
  shared/              apperrors, authz, authctx, password, sse, render, …
web/
  view/                plain view structs; the UI's only data shape
  layouts/ pages/ components/   *.templ
  static/              built css, board.js, vendored js
```

## 3. Dependency rule

`handler → service → domain`. `store.go` implements ports the service declares.

`domain/` imports only the standard library, `google/uuid`, and
`shared/apperrors`. It must never import chi, sqlx, the SQLite driver, templ,
goose, `net/http`, or the env parser. `make check-arch` fails the build on it.

`web/` is a leaf: components take `web/view` structs, never a domain type. A
handler maps domain → view. This is what keeps a template change from touching a
business rule.

## 4. Cross-slice rule

A slice reaches another slice only through its exported service interface. Never
another slice's store, handler, or SQL. When `card` needs to know whether a lane
belongs to a project, it asks `board`; it does not query the `lanes` table.

## 5. One error model

Every layer returns `*apperrors.AppError`. `errors.Is` matches on `Code`, so a
wrapped copy still equals the sentinel — compare against exported `Err…` values,
never against message text. Handlers end in one `fail(w, r, err)` call; there is
no per-package error-to-status switch. A 5xx never carries its cause to the user.

Declare a slice's failures as package-level vars in its `domain` package.

## 6. Identity and keys

**One id: `uuid TEXT NOT NULL PRIMARY KEY`.** A UUIDv7 from
`idgenerator.NewUUIDv7()`, never `uuid.New()`. Foreign keys reference `uuid`
directly.

There is **no** internal integer id. `INTEGER PRIMARY KEY AUTOINCREMENT` is
SQLite-only — PostgreSQL wants `BIGSERIAL`/`IDENTITY` and there is no syntax both
accept — so the dual-id scheme and a portable schema are mutually exclusive.
Portability won. Do not reintroduce a surrogate key: it breaks
`make check-postgres`, and at this scale the 8-vs-36-byte foreign key buys
nothing.

v7 rather than v4 because these are the actual keys: v4 scatters every insert
across the B-tree, v7 leads with a millisecond timestamp so successive inserts
land together.

Because parents are addressed by the same id the child stores, a write is a plain
`INSERT`; a missing parent surfaces as a foreign-key violation, which
`database.MapError` turns into a 400.

## 7. The SQL must run on PostgreSQL

The same migrations must apply to PostgreSQL unchanged. `make check-postgres`
applies them to a real server and fails if they do not.

Use only what both engines accept:

| Use | Never |
|---|---|
| `TEXT` primary keys | `AUTOINCREMENT`, `BIGSERIAL`, `IDENTITY` |
| `TIMESTAMPTZ` via `database.Time` | a bare `time.Time`, a unix integer, a column default |
| `BOOLEAN` with `TRUE` / `FALSE` | `1` / `0` |
| `TEXT` + named `CHECK (… IN (…))` | a PostgreSQL `ENUM` type |
| lower-cased storage + `CHECK (x = lower(x))` | `COLLATE NOCASE`, `CITEXT` |
| `TEXT` for JSON | `JSONB`, `JSON` |
| `INSERT … RETURNING` | `LastInsertId()` |
| `ON CONFLICT (cols) DO UPDATE … excluded` | `INSERT OR REPLACE`, `ON DUPLICATE KEY` |
| `CREATE INDEX … WHERE …` | `WITHOUT ROWID`, `DEFERRABLE` |
| named parameters `:uuid` | `?`, `$1` |

Name **every** `CHECK` with an explicit `CONSTRAINT` clause: SQLite reports a named
constraint's name and an anonymous one's whole expression, and
`database.Constraint(err)` needs the name.

Dialect-specific code is confined to `internal/platform/database/` and each such
file says so at the top. Full contract:
`.project-doc/docs/PROJECT_DATABASE.md`.

## 8. How a store talks to the database

- Named parameters through `database.GetNamed`, `SelectNamed`, `ExecNamed`. An
  `IN (…)` list through `database.SelectIn`.
- **Absence is `(nil, nil)`**, never a sentinel. `sql.ErrNoRows` never escapes a
  store.
- **A statement that changed nothing is an error.** Run the row count through
  `database.RequireRow`. An `UPDATE` matching nothing means the row went away
  under the caller; calling that success loses the write silently.
- **Scoping is authorisation.** A mutation on a project's contents carries
  `AND project_uuid = :project_uuid`, so an id from another board matches nothing
  and the row count refuses it. Never filter for ownership after the fact.
- Reads and writes go through `TxManager`, never `db.Writer()` / `db.Reader()`
  directly — inside a transaction the manager returns the transaction, and the
  read pool cannot see uncommitted rows.

## 9. SQLite rules

- Driver is `modernc.org/sqlite` (pure Go). `CGO_ENABLED=0` everywhere; do not
  introduce `mattn/go-sqlite3`.
- **Two pools, one file.** A writer pool with `SetMaxOpenConns(1)` and a reader
  pool. SQLite permits one writer; serialising writes in Go turns `SQLITE_BUSY`
  into a queue. Readers stay concurrent under WAL, which matters because SSE
  holds connections open.
- Pragmas are not optional: `journal_mode(WAL)`, `busy_timeout(5000)`,
  `foreign_keys(ON)`, `synchronous(NORMAL)`. **`foreign_keys` is off by default in
  SQLite** — without it every FK in the schema is decoration.
- Never `SELECT *`. Name a column projection constant next to the model.
- `Open` verifies the pragmas took effect on both pools. SQLite silently ignores
  a pragma it cannot parse, so this is not redundant — do not remove it.
- An in-memory database is refused: two pools would each get their own private
  empty database. Tests use a real file in `t.TempDir()` via
  `internal/platform/database/dbtest`.
- "Not found" from a store is `(nil, nil)`, never a sentinel error. The service
  decides whether absence is an error.
- Snapshot backups are `VACUUM INTO`. Copying a live WAL database is not a backup.

## 10. Migrations

Goose, SQL only, in `migrations/`, embedded with `go:embed` and run as a library
by the binary — there is no goose CLI in the image. `make migrate-create NAME=…`.

Every migration has a real `-- +goose Down`, and a test rolls the whole set back
and asserts no table survives. A test also asserts the embedded set matches the
on-disk directory, so a migration added but never rebuilt cannot go unnoticed.

Timestamps are `TIMESTAMPTZ` columns written through `database.Time`. One format,
everywhere, never mixed with unix integers — see §7.

Do not add an index on the `uuid` primary key: it already has one.

`(lane_uuid, position)` is indexed but **not unique** — a reorder rewrites every
position in a loop and a unique constraint would fire on an intermediate state
part-way through the transaction. The same applies to `(project_uuid, position)`
on `lanes`.

Run `make check-postgres` after touching anything in `migrations/`.

## 11. Card ordering

`position` is a dense, zero-based integer per lane, renumbered on every move
inside one transaction. The client sends the order it is already displaying;
agreeing with it is the contract. No fractional ranking, no LexoRank, no
rebalancing.

All of the rules live in `internal/card/domain.Plan`, which is pure and has no
database. Change ordering behaviour there, with a test, never in a handler.

## 12. Authentication and authorization

- Cookie sessions, not JWTs. 32 random bytes; only the SHA-256 lands in the
  `sessions` table. `HttpOnly`, `SameSite=Lax`, `Secure` off only for local.
- Argon2id for passwords (lifted from `go-kit/internal/shared/password`).
- Per-session CSRF token, emitted once as `hx-headers` on `<body>`, compared in
  constant time on every non-GET.
- Every permission decision resolves in `internal/shared/authz`, taking
  `(CurrentUser, projectRole, action)`. A handler calls it; it never re-derives a
  rule inline. Adding a role means editing one file and one table test.
- A control hidden in the UI must also be refused at the route. Assume a member
  will `curl` it, because verifying that is part of the test plan.

## 13. HTML, HTMX, and the front end

- templ only. A handler returns a full page or exactly one fragment component.
- Zero-JS is not the goal here (it is an app, not a marketing page), but the
  budget is: htmx + SSE ext + Alpine + Sortable, vendored and pinned, ~49KB
  gzipped in total. Adding a fifth library needs a reason in the PR description.
- No CDN. Everything is same-origin so the CSP stays tight.
- Tailwind utilities in markup, tokens in `@theme`. `@apply` appears only inside
  `@utility` blocks in `input.css`. No `tailwind.config.js` — this is v4.
- A class name assembled at runtime is invisible to Tailwind's scanner. Helpers
  that pick a class return the **whole literal** (see `web/components/helpers.go`).
- **Live updates broadcast whole lanes, not single cards.** An out-of-band
  `outerHTML` swap replaces an element where it already sits, so it cannot
  relocate a card into another lane's container. Re-rendering both affected lanes
  is correct by construction.
- The drag guard hooks `htmx:sseBeforeMessage`, not `htmx:beforeSwap`: the SSE
  extension calls htmx's swap directly and the request-pipeline events never fire.
- Every drag has a keyboard equivalent. A board that can only be dragged is
  unusable without a pointer.

## 14. Tests

Table-driven. In-memory fakes implementing the port; never a mocked database —
against a real temp-file SQLite, because it is a file and there is nothing to
fake. Assert with `errors.Is` against the exported sentinel, never on message
text. One test per behaviour, not per function.

Use a fake hasher everywhere except the password tests: real Argon2id costs 64MB
a call and will make the suite crawl.

The authz matrix table test is the most important test in this repo. It *is* the
requirement.

## 15. Gates

`make check` = `fmt-check` + `vet` + `check-arch` + `go test -race`. It must pass
before any work is called done. `make build` regenerates templ and the stylesheet
first, so a stale generated file cannot ship.

`make check-postgres` is separate because it needs docker. Run it after touching
`migrations/` or `internal/platform/database/`. It is the only thing that proves
the schema is still portable.

## 16. House rules

- Never add a comment that restates the code. Comment the *why* — a constraint, a
  trade-off, a rule that is not obvious from the call.
- Never create a README or docs file unless asked.
- Never commit unless asked. Never commit on `main`; branch first.
