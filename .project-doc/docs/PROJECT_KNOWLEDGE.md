# Project knowledge

Start here. Architecture, conventions, and the traps that have already cost time.

## What this is

A self-hosted kanban board. The owner is the only administrator: they create
projects, define each project's lanes (named whatever they want — `backlog`,
`in-progress`, `testing`, `done`, `stash`), create user accounts by hand, hand out
credentials, reset passwords, and grant each person access to specific projects.
Those people create cards, edit cards, and drag cards. They cannot change anything
about the board's shape.

The brief and its testable restatement are in `../context/BRIEF.md`.

## Stack

Go 1.27 · chi v5 · sqlx · SQLite (`modernc.org/sqlite`, pure Go) · goose (as a
library) · templ · Tailwind v4 (standalone CLI, no Node) · HTMX · Alpine ·
SortableJS · Docker + compose · Makefile.

**The SQL is written to run on PostgreSQL too**, and `make check-postgres` proves
it by applying the real migrations to a real server. Everything dialect-specific
lives in `internal/platform/database/` and says so at the top of the file. See
`PROJECT_DATABASE.md` for the portability contract.

No Postgres, no Redis, no NATS, no SPA, no Node runtime, no bundler. One binary,
one file.

## Architecture

**Pragmatic layered vertical slices.** Five feature slices, each with the same
four parts, plus `platform/` for technical infrastructure and `shared/` for
genuinely cross-slice abstractions.

```
cmd/chonkboard/        composition root + migrate/seed/backup subcommands
migrations/            goose SQL, embedded via go:embed
internal/
  auth/ project/ board/ card/ admin/
    domain/            entities and rules — no framework imports, no database
    service/           use cases; declares the ports it needs
    handler/           chi handlers, form decoding, rendering
    store.go           the SQLite implementation of this slice's ports
  platform/            config, sqlite, httpserver, middleware, backup
  shared/              apperrors, authz, authctx, password, sse, render, logger, …
web/
  view/                plain view structs — the UI's only data shape
  layouts/ pages/ components/     *.templ
  static/              built css, board.js, vendored js
```

| Slice | Owns |
|---|---|
| `auth` | login, sessions, the cookie, own password, first-boot bootstrap |
| `project` | projects, membership grants |
| `board` | lanes, labels |
| `card` | cards, ordering, comments, attachments, activity |
| `admin` | user CRUD, password reset, project grants |

### The two rules that matter

**Dependency:** `handler → service → domain`. `store.go` implements ports the
service declares. `domain/` imports only the standard library, `google/uuid`, and
`shared/apperrors`. `make check-arch` fails the build otherwise.

**Cross-slice:** a slice reaches another slice only through its exported service
interface — never another slice's store, handler, or SQL. When `card` needs to know
whether a lane belongs to a project, it asks `board`; it does not query the `lanes`
table.

`web/` is a leaf. Components take `web/view` structs, never a domain type. A
handler maps domain → view. That is what stops a template change from touching a
business rule.

### Why not the full go-kit pattern

`go-kit/AGENTS.md` in this workspace prescribes bounded contexts with a
CommandBus, a QueryBus, and an event bus. For a single-purpose board that roughly
doubles the file count and buys nothing: there is no second consumer of a card
event and no service to extract.

Kept from go-kit: consumer-declared ports, the single `AppError` model, the
uuid/rowid id split, goose conventions, table-driven tests with in-memory fakes,
and the `check-arch` import gate. Dropped: the buses and the ceremony.

## What was borrowed, and from where

This workspace had **no** precedent for SQLite, templ, HTMX, Alpine, server-side Go
templating, `go:embed`, or a Tailwind build outside npm. Chonkboard sets those
patterns. What it inherited:

| From | What |
|---|---|
| `go-kit/internal/shared/password/password.go` | Argon2id hasher, OWASP params, constant-time verify, PHC encoding — copied |
| `go-kit/internal/shared/apperrors/` | the single `AppError` model where `errors.Is` matches on `Code` |
| `go-kit/internal/platform/database/transaction.go` | the `Executor` interface + `TxManager` shape |
| `go-kit/Makefile` | `check-arch` and the self-documenting `##` help target |
| `go-kit/AGENTS.md` §3 §4 §5 §10 §11 | ports, error model, id split, goose rules, test shape |
| `go-chi-hex/.air.toml` | the hot-reload config shape |

Deliberately **not** taken from go-kit: JWT/ES256 (a server-rendered app wants
cookie sessions), Redis (session revocation is a row update), and the
`id`/`uuid` dual-key scheme from §5 (no portable DDL — see the traps below).

## Conventions

- **Error model.** Every layer returns `*apperrors.AppError`. `errors.Is` matches
  on `Code`, so a wrapped copy still equals the sentinel — compare against exported
  `Err…` values, never message text. Handlers end in one `fail(w, r, err)`. No
  per-package error-to-status switch. A 5xx never carries its cause to the user.
- **Identity.** `uuid TEXT NOT NULL PRIMARY KEY`, a UUIDv7 from
  `idgenerator.NewUUIDv7()` — never `uuid.New()`. Foreign keys reference `uuid`
  directly; **there is no internal integer id.** That is a deliberate reversal of
  go-kit's dual-id scheme, forced by PostgreSQL portability: see
  `../memory/DECISIONS.md`.
- **Timestamps.** Always `database.Time` / `database.NullTime`, never a bare
  `time.Time` in a model. The driver will not convert one for a `TIMESTAMPTZ`
  column, and the format is load-bearing for ordering. `PROJECT_DATABASE.md`
  explains why.
- **Queries.** Named parameters (`:uuid`) through `database.GetNamed`,
  `SelectNamed`, `ExecNamed`. Never `?` or `$1` in a store.
- **Absence.** A store returns `(nil, nil)` for "not found", never a sentinel. The
  service decides whether absence is an error.
- **Ordering.** `position` is a dense, zero-based integer per lane, renumbered on
  every move in one transaction. All of the rules live in
  `internal/card/domain.Plan`, which is pure. Change ordering there, with a test,
  never in a handler.
- **Tests.** Table-driven. In-memory fakes for ports; a real temp-file database for
  stores. `errors.Is` against the sentinel. A fake hasher everywhere except the
  password tests.
- **Comments.** Comment the *why* — a constraint, a trade-off, a rule that is not
  obvious from the call. Never restate the code.

## Traps


### 1. `foreign_keys` is OFF by default in SQLite
`foreign_keys` is OFF by default in SQLite
`foreign_keys` is OFF by default in SQLite

Every foreign key in the schema is decoration without
`_pragma=foreign_keys(ON)` on the DSN. There is a store test that deliberately
violates an FK and asserts it is raised, purely to prove the pragma is on.

### 2. SQLite has exactly one writer
SQLite has exactly one writer
SQLite has exactly one writer

A single connection pool with default settings produces `SQLITE_BUSY` under
concurrent writes — which will not happen at your desk and will happen in use. Two
pools: a writer with `SetMaxOpenConns(1)`, a reader with more. Use the right one.
Reads stay concurrent under WAL, which matters because an SSE stream holds a
connection for as long as a board is open.

### 3. `INTEGER PRIMARY KEY AUTOINCREMENT` is not portable
`INTEGER PRIMARY KEY AUTOINCREMENT` is not portable
`INTEGER PRIMARY KEY AUTOINCREMENT` is not portable

SQLite auto-assigns only for `INTEGER PRIMARY KEY`; PostgreSQL wants `BIGSERIAL`
or `IDENTITY` and does not know `AUTOINCREMENT`. There is no common syntax, which
is why the schema has no integer surrogate key at all — `uuid TEXT PRIMARY KEY` is
the only key. Do not reintroduce one "for performance": it breaks the portability
gate, and at this scale it buys nothing.

### 4. The driver will not store a `time.Time` for you
The driver will not store a `time.Time` for you
The driver will not store a `time.Time` for you

`modernc.org/sqlite` does not recognise a `TIMESTAMPTZ` declared type. A bare
`time.Time` is stored as Go's `time.String()` (`2026-09-25 19:51:53.518063 +0000
UTC`), which cannot be scanned back and is not valid PostgreSQL input either.
Always use `database.Time` / `database.NullTime`.

### 5. A trimmed timestamp fraction sorts wrongly
A trimmed timestamp fraction sorts wrongly
A trimmed timestamp fraction sorts wrongly

SQLite compares these columns as text, so `ORDER BY created_at` is lexicographic.
`time.RFC3339Nano` trims trailing zeros and `.` (0x2E) sorts before `Z` (0x5A), so
`…:53.5Z` sorts *before* `…:53Z`. Rows come back in the wrong order with no error.
`database.Layout` is fixed-width for exactly this reason, and a test asserts the
broken ordering RFC3339Nano would produce.

### 6. sqlx does not know the driver name `"sqlite"`
sqlx does not know the driver name `"sqlite"`
sqlx does not know the driver name `"sqlite"`

`BindType` returns UNKNOWN, so `Rebind` hands the query back untouched — which
*works*, because SQLite's placeholder already is `?`. It would break silently the
day the driver becomes `pgx`. The package registers it in `init` with
`sqlx.BindDriver`; do not remove that.

### 7. A pragma SQLite cannot parse is silently ignored
A pragma SQLite cannot parse is silently ignored
A pragma SQLite cannot parse is silently ignored

Which is why `Open` reads every pragma back on both pools and refuses to start if
one did not take. Do not "simplify" that away.

### 8. A port that re-resolves its own authorisation breaks on routes without that parameter
A port that re-resolves its own authorisation breaks on routes without that parameter
A port that re-resolves its own authorisation breaks on routes without that parameter

`BoardShape.LaneSummaries` originally took `*http.Request` and called
`Resolve(r)`, which reads `chi.URLParam(r, "project")`. On `GET /` — the project
list — there is no such parameter, so every board came back "no such project".
Pass the already-resolved subject in; a port must not re-derive context its caller
already has.

### 9. Go's per-package coverage understates a handler tested through the router
Go's per-package coverage understates a handler tested through the router
Go's per-package coverage understates a handler tested through the router

Coverage of another package is credited to the package the *test* lives in. The
project and board handlers are driven from `httpserver`'s tests, so they read ~40%
per-package and ~95% under `make cover`, which uses `-coverpkg`. Do not chase the
per-package number.

### 10. `-race` plus `-coverpkg ./...` produces a zero-count profile
`-race` plus `-coverpkg ./...` produces a zero-count profile
`-race` plus `-coverpkg ./...` produces a zero-count profile

~2200 lines of profile, every counter zero, total 0.0%. `make cover` omits `-race`
for exactly this reason; `make check` still runs the race detector.

### 11. A method value on a nil pointer registers fine and panics on the first request
A method value on a nil pointer registers fine and panics on the first request
A method value on a nil pointer registers fine and panics on the first request

`r.Get("/", d.Projects.List)` with a nil `d.Projects` is legal Go. The panic lands
inside a request, pointing at the handler rather than the wiring.
`httpserver.mustHaveDeps` now fails at construction instead.


Every one of these has already cost time, or was found by a test that nearly did
not exist. Read them even if your task looks unrelated.

### 12. A repeated form field with an empty value is a one-element slice
A repeated form field with an empty value is a one-element slice

`from_order=` gives `[]string{""}`, not nil. Handed to the move planner that empty
string is a card uuid not on the board, so the move is refused — and the case it
breaks is dragging the last card out of a lane. `board.js` omits the field entirely,
so this is invisible through the real client. Filter empties from any repeated field
of identifiers.

### 13. "What changed" derived from "what was written" misses what became absent
"What changed" derived from "what was written" misses what became absent

`domain.AffectedLanes` reads the placements a plan produced, so a lane whose new order
is empty appears unaffected — exactly the lane a card was dragged out of, which has
visibly changed. `card.Service.Move` adds the previous lane explicitly.

### 14. A UUIDv7 prefix is not a usable short id
A UUIDv7 prefix is not a usable short id

Two rows created in the same millisecond share their first 8 hex characters, because
v7 leads with a timestamp — which is the whole reason for choosing it. A debug script
keyed on `uuid[:8]` will silently merge them.

### 15. An out-of-band swap cannot relocate an element
An out-of-band swap cannot relocate an element
An out-of-band swap cannot relocate an element

`hx-swap-oob="outerHTML"` replaces an element **where it already sits**. It cannot
move a card from one lane's container into another's. This is why live updates
broadcast whole lanes rather than single cards — correct by construction, a few
hundred bytes. The plan originally said single cards; that was wrong.

### 16. `htmx:beforeSwap` never fires for an SSE message
`htmx:beforeSwap` never fires for an SSE message
`htmx:beforeSwap` never fires for an SSE message

The SSE extension calls htmx's `swap()` directly rather than going through the
request pipeline, so the request-pipeline swap events do not fire. The cancelable
event is **`htmx:sseBeforeMessage`**. That is where the drag guard hooks; hooking
the wrong one gives a guard that silently never runs, and a card that gets yanked
out from under the pointer.

Related, and verified by reading the vendored htmx source rather than assumed:
htmx's out-of-band pass runs **before** the main swap, so `hx-swap="none"` still
applies OOB content. That is the whole basis of the live board.

### 17. A class name assembled at runtime is invisible to Tailwind
A class name assembled at runtime is invisible to Tailwind
A class name assembled at runtime is invisible to Tailwind

Tailwind scans source text. `"bg-lane-" + color` produces nothing. Helpers that
pick a class return the **whole literal** — see `web/components/helpers.go`. The
failure mode is an element that is silently unstyled, with no error anywhere.

### 18. In Tailwind v4, `@apply` takes only real utilities
In Tailwind v4, `@apply` takes only real utilities
In Tailwind v4, `@apply` takes only real utilities

A bare class in `@layer components` cannot be `@apply`-ed; the build fails with
"Cannot apply unknown utility class". Declare it with `@utility` instead. That is
why `btn`, `field`, and `form-label` are `@utility` blocks in `input.css`.

### 19. templ's `attr?={ }` takes a bool and cannot omit a value-carrying attribute
templ's `attr?={ }` takes a bool and cannot omit a value-carrying attribute

It toggles presence for *boolean* attributes. Given a string it reports "non-boolean
condition in if statement", pointing at generated code. There is no way to spell
"omit `aria-describedby` entirely unless there is an error" with it — use a
`templ.Attributes` spread from a plain `.go` helper, and let the helper own the whole
attribute, because HTML keeps only one of a duplicate and silently drops the other.

### 20. Do not import `github.com/a-h/templ` in a `.templ` file
Do not import `github.com/a-h/templ` in a `.templ` file
Do not import `github.com/a-h/templ` in a `.templ` file

templ injects that import into the generated code. Importing it yourself gives
"templ redeclared in this block". `templ.Attributes` is available without it.

### 21. `templ generate` before `go build`
`templ generate` before `go build`
`templ generate` before `go build`

A bare `go build` compiles against whatever was generated last. `make build`,
`make dev`, and the Dockerfile all sequence it correctly. `*_templ.go` is
gitignored, so a fresh clone that skips generate will not compile at all — which is
the better failure.

### 22. Echo suppression must be per tab, not per session
Echo suppression must be per tab, not per session
Echo suppression must be per tab, not per session

Two tabs of the same account share a session. Suppressing a broadcast by session id
leaves the second tab stale. `board.js` generates a per-tab id, sends it as
`X-Client-Id`, and the hub skips exactly that subscriber.

### 23. A non-blocking send still races a close
A non-blocking send still races a close
A non-blocking send still races a close

The SSE hub originally snapshotted its subscribers, released the lock, then sent.
A concurrent drop could close a channel between those two steps — `panic: send on
closed channel`, found by a `-race` test with 50 goroutines. Sends now happen
**under the read lock**, which is safe precisely because they cannot block: the
`select` has a `default`. Holding the read lock excludes `remove()`, which is the
only thing that closes a channel.

### 24. Every line of an SSE payload needs its own `data:` prefix
Every line of an SSE payload needs its own `data:` prefix
Every line of an SSE payload needs its own `data:` prefix

An HTML fragment is multi-line. A bare newline inside `data:` terminates the event,
delivering truncated markup that swaps successfully and looks like a rendering bug.
`sse.encode` handles it and has a test.

### 25. `vendor/` in `.gitignore` is not anchored
`vendor/` in `.gitignore` is not anchored
`vendor/` in `.gitignore` is not anchored

An unanchored `vendor/` also matches `web/static/vendor/`, silently excluding the
pinned front-end libraries that are the entire point of vendoring. It is `/vendor/`.

### 26. Hiding a control is not authorization
Hiding a control is not authorization
Hiding a control is not authorization

Every board-settings route is behind a manager-or-above check so a hand-made
`POST` with a member's cookie returns 403. The test plan verifies this with
`curl`, because assuming it is how it stops being true.

### 27. Go durations have no day unit
Go durations have no day unit
Go durations have no day unit

`SESSION_TTL=7d` does not parse. Seven days is `168h`.

## Where things are

| Looking for | File |
|---|---|
| Binding architecture rules | `../../AGENTS.md` |
| Card ordering rules, pure and tested | `internal/card/domain/move.go` |
| Pools, pragmas, transactions | `internal/platform/database/database.go`, `tx.go` |
| The portable timestamp type | `internal/platform/database/timestamp.go` |
| Driver error → `AppError` (dialect-specific) | `internal/platform/database/errors.go` |
| Query helpers every store uses | `internal/platform/database/query.go` |
| PostgreSQL portability proof | `internal/platform/database/postgres_test.go` |
| Migrations | `migrations/*.sql` |
| SSE fan-out and wire format | `internal/shared/sse/{hub,handler}.go` |
| The error model | `internal/shared/apperrors/errors.go` |
| Permission matrix (code) | `internal/shared/authz` |
| Project access resolution — the one place | `internal/project/service.go` `Resolve` |
| The lane rules and the manager wall | `internal/board/service.go` |
| Card create, edit, archive, delete, move | `internal/card/service.go` |
| The board page and the lane fragment | `internal/card/handler.go` |
| The modal shell and its focus trap | `web/components/modal.templ` |
| Lane and project forms | `web/pages/{projects,project_settings}.templ` |
| CSRF token for a fragment's form | `web/view/csrf.go` |
| Argon2id hashing, and the test fake | `internal/shared/password` |
| Sessions, tokens, the cookie | `internal/auth/{service,token,cookie}.go` |
| Session / CSRF / rate-limit middleware | `internal/platform/middleware/` |
| Current user on the context | `internal/shared/authctx` |
| Sign-in throttle | `internal/shared/ratelimit` |
| Permission matrix (doc) | `AUTH_FLOWS.md` |
| Asset embedding, cache busting | `web/assets.go` |
| Configuration | `internal/platform/config/config.go` |
| Design tokens — single source | `web/css/input.css` `@theme` |
| Design system, contrast table | `../design/DESIGN-GUIDELINES.md` |
| Drag + live-update client | `web/static/js/board.js` |
| Route table | `ROUTES.md` |
| Schema | `PROJECT_DATABASE.md` |
| Docker, Make, env, backups | `PROJECT_INFRA.md` |
| Front-end contract and budget | `FRONTEND.md` |
| Phases and gates | `../plan/ROADMAP.md` |
| What is actually built | `../status/STATUS.md` |

## Temporary scaffolding

**None.** The phase-0 in-memory board (`devboard.go`) was deleted in phase 4, once
the card service and handler were behind the real routes. Nothing from the spike
remains on any route.

