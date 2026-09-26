# Roadmap

Nine phases. Each one ends **green**: `make check` passes and the phase's stated
behaviour is demonstrable in a browser. Nothing is left half-wired across a phase
boundary, because a half-wired slice is indistinguishable from a broken one three
days later.

Phases are ordered by dependency, not by visibility. The data layer comes before
auth because sessions are rows; auth comes before projects because every project
route is behind a session; cards come before the live board because there is
nothing to broadcast until something can change.

| # | Phase | State |
|---|---|---|
| 0 | Scaffold and spike | **done** (2026-09-26) |
| 1 | Data layer | **done** (2026-09-26) |
| 2 | Auth and sessions | **done** (2026-09-26) |
| 3 | Projects, lanes, membership | **done** (2026-09-26) |
| 4 | Cards core | **done** (2026-09-26) |
| 5 | Rich card | **done** (2026-09-26) |
| 6 | Live board | **done** (2026-09-26) |
| 7 | Admin console | next |
| 8 | Harden and ship | not started |

---

## Phase 0 — Scaffold and spike · done

**Why first.** One mechanism in this design could not be assumed: whether htmx
applies an out-of-band swap from a payload delivered over SSE. Everything about
the live board rests on it, so it was proven before anything was built on top.

**Delivered**

- Repository, Go module, directory skeleton, `.gitignore`, `.env.example`
- `Makefile` (30 targets, self-documenting `make help`), `.air.toml` for hot
  reload, three-stage `Dockerfile`, single-service `docker-compose.yml`
- Tailwind v4 standalone CLI in `bin/`; token stylesheet with a measured contrast
  table, a dark scheme, and one `prefers-reduced-motion` block
- Vendored and pinned front end: htmx 2.0.8, htmx-ext-sse 2.2.4, Alpine 3.15.1,
  SortableJS 1.15.6 — 49KB gzipped in total
- templ layouts and the board/lane/card/toast components; `web/view` models
- `web/assets.go` — `go:embed`, content-hashed cache busting, immutable caching,
  directory listings refused
- `internal/shared/apperrors` — the single error model
- `internal/shared/sse` — hub and stream handler, 10 tests
- `internal/shared/render`, `internal/shared/logger`
- `internal/platform/httpserver` and `middleware` — chi router, CSP and security
  headers, request logging, graceful shutdown with no write timeout on SSE
- `internal/card/domain/move.go` — the pure ordering planner, 15 table cases
- `web/static/js/board.js` — Sortable wiring, client-side WIP pre-check, drag
  guard, server-authoritative revert

**Gate — met.** `make check` green. Over HTTP: a cross-lane move persists with
dense positions in the order sent; it broadcasts both rewritten lanes as one
`lane-updated` event with correct `hx-swap-oob` selectors; the originating tab
receives zero echoes; a WIP breach returns 409 with a toast; a foreign card
returns 404. The htmx out-of-band pass was confirmed by reading the vendored
source to run *before* the main swap, so `hx-swap="none"` still applies OOB.

**Outstanding.** The drag gesture itself and the visual swap need a browser; no
browser tooling was available. See `../status/STATUS.md`.

**Left behind, since deleted.** An in-memory board (`devboard.go`) existed only so
the spike could be end-to-end. It called the real `domain.Plan`, so what was proven
was what ships. Phase 3 took it off the page path and **phase 4 deleted it**, along
with the `Board` field on `httpserver.Deps`.

---

## Phase 1 — Data layer · done

**Why here.** Everything above it is rows. Getting the SQLite setup wrong is the
kind of mistake that shows up as intermittent `SQLITE_BUSY` in production and
never at a desk, so it was worth doing before there was anything to debug
alongside it.

**Scope changed mid-phase.** The owner added a requirement: the migrations must be
SQL that later runs on PostgreSQL unchanged. That forced a documented decision to
be reversed — `INTEGER PRIMARY KEY AUTOINCREMENT` has no PostgreSQL equivalent, so
the `id`/`uuid` dual-key scheme inherited from `go-kit/AGENTS.md` §5 was dropped
and UUIDv7 became the sole primary key. Rationale in `../memory/DECISIONS.md`.

**Delivered**

- `migrations/` — five goose files, each with a real `-- +goose Down`, written
  against the DDL subset SQLite and PostgreSQL both accept. Eleven tables.
  `migrations/embed.go` holds the `go:embed` filesystem.
- `internal/platform/database/` (named `database`, not `sqlite`, because the
  dialect is now a seam rather than an assumption):
  - `database.go` — two pools on one file, writer pinned to one connection, and
    **pragma verification** on both pools at open
  - `dsn.go` — the pragma DSN; refuses an in-memory database
  - `timestamp.go` — `Time` / `NullTime`, the fixed-width portable format
  - `tx.go` — `Executor`, `TxManager`, context-carried transactions, nested joins
  - `query.go` — `GetNamed` / `SelectNamed` / `ExecNamed` / `SelectIn` /
    `RequireRow`: one definition of absence and of "changed nothing"
  - `errors.go` — SQLite result codes → `AppError`, plus four predicates and
    `Constraint(err)`. Marked as the file PostgreSQL will rewrite
  - `migrate.go` — goose as a library; `ApplyMigrations(…, dialect, …)` so the
    dialect is an argument in exactly one place
  - `dbtest/` — migrated throwaway databases on a real temp file
- `internal/platform/config/` — flat struct, `envDefault` on every field,
  validation that names the variable
- `internal/shared/idgenerator/` — `NewUUIDv7`, `Valid`
- Domain entities for all four slices: `auth` (User, Session), `project`
  (Project, Membership, ProjectRole), `board` (Lane, Label, Colour, Reorder),
  `card` (Card, Priority) beside the existing move planner
- A store per slice: `auth` (users + sessions), `project` (projects + grants),
  `board` (lanes + labels), `card` (cards + label assignments + activity,
  including the transactional `Move` that drives `domain.Plan`)
- `cmd/chonkboard/migrate.go` — `migrate up|down|reset|status|version|create`,
  sharing one code path with boot
- `main.go` now loads config, opens the database, and migrates at boot
- Makefile: `migrate-reset`, `check-postgres`

**Gate — met.**

`make check` green: `gofmt`, `go vet`, `check-arch`, `go test -race`. 145 tests.
Domain coverage 97–100% per slice; stores 81–88%.

Verified against a real file, not only in tests:

| Check | Result |
|---|---|
| `migrate up` from an empty file | version 5, eleven tables |
| `migrate reset` | every table gone — no `Down` section is a stub |
| `migrate up` again | clean |
| `migrate down` then `status` | version 4, `00005` shows Pending |
| `migrate create` | numbered `00006`, correct template |
| Bad configuration | refused at startup, naming the variable |
| A foreign-key violation is actually raised | yes — proves the pragma is on |
| `RowsAffected() == 0` on a missing parent | surfaces as not-found, not silent success |

**Portability — verified, not asserted.** `make check-postgres` starts
`postgres:17-alpine`, applies these exact migrations with goose's PostgreSQL
dialect, and passes 9 tests: every migration applies, every rollback applies, the
columns get native `timestamptz`/`boolean` types, the timestamp format round-trips
through a real `timestamptz`, nullable timestamps round-trip, `ORDER BY` is
chronological, the constraints all fire, the portable upsert works, and the partial
index exists. Ran green against PostgreSQL 17.11.

**Not done here, deliberately.** Stores for `comments` and `attachments` land with
phase 5, where there is a service to use them. The tables and their constraints
exist and are exercised by the cascade tests; writing speculative query shapes for
them now would mean guessing.

**Watch for.** Four of the traps in `../docs/PROJECT_KNOWLEDGE.md` came out of this
phase and are not obvious: the driver will not store a `time.Time` for a
`TIMESTAMPTZ` column, a trimmed fraction sorts wrongly, sqlx does not know the
driver name `"sqlite"`, and SQLite silently ignores a pragma it cannot parse.

---

## Phase 2 — Auth and sessions · done

**Why here.** Every route after this one is behind a session, and the permission
model is the requirement, so it was worth having before there was anything to
protect.

**Delivered**

- `internal/shared/password` — Argon2id lifted from
  `go-kit/internal/shared/password/`, OWASP parameters (`t=3, m=64MB, p=4`),
  PHC-encoded, constant-time verify, and a `Fake` for every other package's tests
- `internal/shared/authz` — the whole permission matrix as
  `Subject.Can(action)` / `CanOn(action, ownerUUID)`, 100% covered
- `internal/shared/authctx` — the user and session on the request context
- `internal/shared/ratelimit` — token bucket with an injectable clock and a
  sweeper, 100% covered
- `internal/auth/domain/password.go` — the length-only policy and the
  confusable-free generated password
- `internal/auth/service.go` — login, authenticate, logout, change own password,
  super-admin bootstrap, expired-session reaping
- `internal/auth/token.go` — 32-byte tokens; only the SHA-256 is stored
- `internal/auth/cookie.go` — the session cookie, owned by this slice
- `internal/auth/handler.go` — login page, sign-in, sign-out, account page
- `internal/platform/middleware/{session,csrf,ratelimit}.go`
- `web/layouts/plain.templ`, `web/pages/{login,account}.templ`,
  `web/pages/helpers.go`
- `cmd/chonkboard/serve.go` — composition, first-boot bootstrap, housekeeping
  ticker for idle buckets and expired sessions

**Gate — met.** `make check` green. Verified over real HTTP against a running
binary on an empty database:

| Check | Result |
|---|---|
| First boot on an empty database | one super admin, password printed once to stdout, `must_change_password` set |
| A second boot | creates nothing |
| Signed-out board request | 303 to `/login` |
| `/login`, `/static`, `/healthz` | reachable with no session |
| Wrong password vs unknown address | byte-identical responses apart from the echoed address; both 401 |
| Successful sign-in | 303, `HttpOnly` `SameSite=Lax` `Path=/` cookie, 43-char token |
| `must_change_password` | every route redirects to `/account/password`; that page and `/logout` stay reachable |
| Forged POST, no CSRF token | 403 |
| Forged POST, wrong CSRF token | 403 |
| Correct token via form field, and via header | both accepted |
| Password change | old password 401s, new one works, forced-change flag cleared |
| A second browser during that change | revoked; the browser that made the change is kept |
| Sign-out | session revoked, cookie cleared with matching attributes |
| Suspending an account mid-session | next request refused and the cookie cleared |
| 5 wrong attempts, then a 6th | 401 × 5, then 429 with `Retry-After` |
| The correct password while throttled | also 429, so an attacker cannot tell they found it |
| A different account while one is throttled | unaffected |
| Stored data | Argon2id PHC hash, 64-hex token hashes, no plaintext anywhere |
| The new password in the log | zero occurrences |
| Restart | sessions survive — they are rows, not memory |
| Expired HTMX request | 204 + `HX-Redirect: /login` |
| SIGTERM | graceful exit |

**Watch for.** Two things about this phase are easy to undo by accident: the decoy
hash on the no-account path (without it, timing enumerates accounts even though the
messages match) and the fact that a successful sign-in must reset only the
*account's* rate-limit bucket, never the address's.

---

## Phase 3 — Projects, lanes, membership · done

**Why here.** This is requirement R2 and R6 — the dynamic lanes, and the wall
between a member and the board's shape.

**Delivered**

- `internal/project/service.go` — `Resolve` (slug or uuid → project + grant +
  `authz.Subject`), scoped `List`, `Create` (project + creator's manager grant +
  starting lanes, one transaction), rename, archive, restore, delete, and the
  membership operations
- `internal/project/handler.go` — the project list, the create form, the settings
  page, and membership. Owns access resolution, page chrome and the error path for
  both slices
- `internal/board/service.go` — lane create, update, reorder, delete, the label
  operations, and `SeedDefaultLanes`. Every mutating method takes an
  `authz.Subject` and refuses a non-manager first
- `internal/board/handler.go` — the board page and whole-board fragment, the lane
  forms, the up/down reorder, and the delete-with-`move_to` flow
- `internal/auth/directory.go` — the two read methods the project slice needs for
  a member list, exposed on the auth service rather than its store
- `web/view/csrf.go` — the UI layer's own context carrier for the CSRF token
- `web/pages/{projects,project_settings}.templ`,
  `web/components/{project,lane_settings,member_row}.templ`
- `cmd/chonkboard/seed.go` — `chonkboard seed user <email> [name]`, plus
  `make seed-user`. Not in the original phase list; without it there is no way to
  create a second account before phase 7, so the gate could not be exercised at all
- The board page renders real lanes from the database. The phase-0 in-memory board
  is still present for the card-move spike but is no longer on the page path
  (phase 4 deletes it)
- `httpserver.mustHaveDeps` — the router refuses to start with an unwired handler
- `make cover` now reports honest aggregate coverage; `make cover-html` opens it

**Gate — met.** `make check` green, and walked end to end over HTTP against a
running binary, as a super admin and then as a member:

| Check | Result |
|---|---|
| A new project | five lanes — Backlog, In progress, Testing, Done, Stash — positions 0-4 |
| Rename, recolour, set a WIP limit | applied; `max 3` shown on the settings page |
| Reorder by one place | order changed, positions stayed dense |
| Add a lane | appended at the end |
| A WIP limit of 0, -3, or "lots" | 400, with what was typed still in the form |
| Delete lanes down to the last one | five 303s then a 409, "a board needs at least one lane" |
| Delete a lane holding cards with no target | 409; the lane survives |
| Delete a lane holding cards with a target | cards appended to the target, order kept |
| Rename the board, edit its description | applied |
| A duplicate slug | 409 with a field error on `slug` |
| A malformed slug | 400 for spaces, underscores, `--`, leading `-` |
| Archive | off the default list, on `?archived=1`, hidden from members too |
| Restore | back on the list |
| Delete a project | gone, with zero orphan lanes and zero orphan grants |
| A board by slug, and by uuid | both 200 |
| A project that does not exist | 404 |
| **A member's board** | **200 — the same lanes, colours and WIP limits** |
| **Every lane control rendered for a member** | **none: no Add a lane, no Settings link, no edit link** |
| **A member with no grant** | sees no projects; the board is 404, not 403 |

**The wall, with a valid CSRF token.** Every board-shape route was exercised with
a member's cookie *and* a real token — verified first by a control request that
reached the handler and failed on its own merits, so the 403s below are
authorisation and not the CSRF check:

| Route | Member | Manager |
|---|:--:|:--:|
| `POST /projects/{p}/lanes` | **403** | 303 |
| `POST /projects/{p}/lanes/{l}` | **403** | 303 |
| `POST /projects/{p}/lanes/{l}/move` | **403** | 303 |
| `POST /projects/{p}/lanes/reorder` | **403** | 204 |
| `POST /projects/{p}/lanes/{l}/delete` | **403** | 303 |
| `POST /projects/{p}/settings` | **403** | 303 |
| `POST /projects/{p}/members` | **403** | 303 (member role only) |
| `POST /projects/{p}/members/{u}` | **403** | **403** |
| `POST /projects/{p}/archive` | **403** | **403** |
| `POST /projects/{p}/delete` | **403** | **403** |
| `POST /projects` | **403** | **403** |
| `GET /projects/{p}/settings` | **403** | 200 |
| `GET /projects/{p}/lanes/new` \| `/{l}/edit` \| `/{l}/delete` | **403** | 200 |

Nothing changed after any refusal: the same lanes in the same order, the same
board name, not archived.

**Membership rules, all verified.** Only the operator may create a project, mint a
manager, remove a manager, archive or delete. A manager may add and remove ordinary
members. The last manager cannot be demoted or removed (409). Revoking somebody who
is not a member is 404. A suspended account is never offered as a candidate.

**Deliberately not done.** Labels have a service and store but no UI — they are only
useful once cards can carry them, which is phase 5. Lane drag-reorder is not wired;
the up/down buttons are the keyboard path and the drag shares the same
`ReorderLanes`, so phase 6 adds the gesture without a new endpoint.

---

## Phase 4 — Cards core · done

**Why here.** Requirements R7, R8, R9. The ordering rules already existed and were
tested from phase 0; this phase gave them a database.

**Delivered**

- `internal/card/service.go` — create, edit, archive and restore, delete, move, and
  a card's history. Every method takes an `authz.Subject`; most are permitted to a
  member, because creating, editing and dragging cards is what a member is here to do
- `internal/card/handler.go` — the card routes, the board page and whole-board
  fragment, the lane fragment, the SSE stream, and the broadcast after every mutation
- `internal/card/store.go` — `CompactLane`, so a delete or an archive leaves the
  lane's positions dense
- `internal/auth/directory.go` — `DisplayName`, for the names a card carries
- `web/components/{modal,card_form,card_modal}.templ` — the modal shell with a real
  focus trap, the create/edit form, and the detail panel with its history
- **`devboard.go` is deleted**, along with the `Board` field on `httpserver.Deps`.
  Nothing phase-0 remains on any route
- `view.Board.CanAddCards` is true, so the add-card control appears

**Gate — met.** `make check` green, `make check-postgres` green, and walked over HTTP
against a running binary:

| Check | Result |
|---|---|
| Add three cards to a lane | positions 0, 1, 2 in creation order |
| **Reorder in place** | applied; dense |
| **Across lanes** | both lanes rewritten; dense |
| **Into an empty lane, emptying the source** | applied; both lanes dense |
| **A drag survives a reload** | re-read from the database, unchanged |
| **A WIP breach** | **409**, "already at its limit"; neither lane touched |
| A full lane reordered internally | allowed — the limit gates arrivals, not the column |
| **A stale `from_lane`** | **409**; nothing moved |
| **A lane from another board** | **404** |
| **A card from another board** | **404** |
| An order naming an unknown card | 404 |
| An order missing the moved card | 400 |
| The same card twice | 400 |
| A move with no CSRF token | 403; nothing moved |
| Edit a card | title and description saved, `updated` logged |
| An empty title | 400, with what was typed still in the form; nothing written |
| Archive, then restore | off the board and back; lane dense throughout |
| Delete the middle of five cards | lane renumbered to 0, 1, 2, 3 |
| **A member deleting somebody else's card** | **403**, "only delete your own cards" |
| A member deleting their own | 303 |
| A manager deleting anybody's | 303 |
| A member editing, moving, archiving anybody's card | allowed — it is a shared board |
| An ungranted card, every verb | 404, never 403 |
| The activity log | created, moved (lane changes only), updated, archived, restored |

**Three defects the verification found**, all fixed:

1. `from_order=` with an empty value arrives as a one-element slice holding `""`,
   which the planner rejects — so **dragging the last card out of a lane was
   refused**. Invisible through `board.js`, which omits the field entirely.
2. `AffectedLanes` misses a lane that lost its only card, because it derives from
   placements and an empty order produces none. Other tabs would have gone on showing
   the card in its old lane.
3. Delete and archive left a hole in the position sequence.

**Live updates work now, ahead of phase 6.** The hub, the heartbeat and the
`ExceptClient` echo filter were built in phase 0; wiring the real service into the
existing broadcast was a few lines, and dropping it to "save it for phase 6" would
have been a regression. Phase 6 adds the drag guard, reconnect behaviour and the
`board-dirty` path for structural changes.

**Deliberately not done.** Priority, assignee, due date, labels, comments and
attachments are phase 5 — the domain entity, the store and the card component already
carry all of them, so `EditInput` grows rather than the plumbing around it. The edit
path passes the phase-5 fields back unchanged, so editing a title cannot clear a due
date set elsewhere.

---

## Phase 5 — Rich card · done

**Why here.** The card is the thing people live in. Everything here is additive to a
working board, which is why it came after one existed.

**Delivered**

- `internal/shared/markdown` — goldmark (GFM, **no `WithUnsafe`**) then bluemonday's
  `UGCPolicy`. Raw HTML in a body is escaped before the sanitiser sees it
- `internal/platform/filestore` — attachment bytes on disk under a generated 32-hex
  name, fanned out by first byte, with `pathFor` refusing anything that is not a name
  this package produced
- `internal/card/comments.go` — the comment model, its queries and its rules, with a
  soft delete that leaves a tombstone
- `internal/card/attachments.go` — upload, serve, delete, a MIME allowlist, and the
  per-request authorisation that makes a leaked URL harmless
- `internal/card/service.go` — `EditInput` grown to every rich field, the assignee
  membership check, and `MoveToLane` (the keyboard path, going through the same `Move`)
- `internal/project/service.go` — `CanSee` and `People`, so the card slice can ask who
  may be assigned work
- `internal/platform/middleware/bodylimit.go` — one global request ceiling, applied
  before anything parses a body
- `web/components/{card_modal,card_form,comment,attachment,label,modal}.templ` and the
  `prose-card` rules that style exactly the tag set the sanitiser permits

**Gate — met.** `make check` and `make check-postgres` both green. Walked over HTTP:

| Check | Result |
|---|---|
| A card with priority, due date, assignee, description and two labels | every field round-trips, in the database, on the modal, on the board and back into the edit form |
| Clearing every optional field | assignee and due date become NULL, labels emptied |
| An assignee with no grant on the board | 400 with a field error |
| A label from another board | 404 |
| **Markdown containing `<script>`, `onerror`, `javascript:`, `<iframe>`, `<form>`, `<svg onload>`** | **all inert; the only forms on the page are the application's own** |
| The same, in a comment | inert |
| A comment, then a soft delete | the row stays, renders as "removed a comment", stops counting on the badge |
| An empty comment | 400 |
| A member deleting somebody else's comment | 403 |
| **A permitted upload** | stored under a generated hex name, never the uploader's |
| **A 200KB file against a 64KB ceiling** | **413, "too large"** — no row, no orphan on disk |
| A shell script, HTML, an SVG | 400, "cannot be attached" |
| A filename with a path | reduced to its base |
| Download | `Content-Disposition: attachment`, `nosniff`, bytes match |
| **A leaked attachment URL, fetched by somebody with no access** | **404 — and byte-identical to the response for a made-up id** |
| A member deleting somebody else's attachment | 403 |
| Delete an attachment | row and bytes both gone |
| The "Move to lane…" control | offered, excluding the lane the card is in |
| A move through it, into a full lane | 409, "already at its limit" — the same rules as a drag |
| Label create, rename, delete by a manager | works; a duplicate name is 409 |
| The same by a member | 403 — but attaching an existing label is allowed |
| Deleting a label | removed from every card that carried it |
| Positions after all of it | dense in every lane |

**One defect, and it was total.** `r.ParseForm` reads only URL-encoded bodies, so the
CSRF check found no token in a multipart request and **every upload returned 403**.
Invisible to a JavaScript client sending the token as a header, and invisible to a test
that posts a URL-encoded form and calls it an upload. Fixing it required a global body
ceiling first, because parsing a multipart body spools it to disk — so
`middleware.LimitBody`, planned for phase 8, landed here.

**Not verified.** The modal's focus trap is `x-trap.noscroll` from Alpine and cannot be
exercised without a browser. It is part of the outstanding browser check.

---

## Phase 6 — Live board · done

**Why here.** The hub and the wire format existed from phase 0; this is the phase that
connected them to real mutations — and most of the connecting had already happened in
phase 4, where dropping working broadcast to "save it for phase 6" would have been a
regression.

**Already in place before this phase.** The hub with its per-room fan-out, the
heartbeat, the `ExceptClient` echo filter, the SSE wire encoder, `X-Client-Id` threaded
from `board.js` through the move, the drag guard on `htmx:sseBeforeMessage`, and the
broadcast after every card mutation.

**Delivered here**

- **`board-dirty` on every structural change** — lane added, renamed, recoloured,
  reordered, deleted; label added, renamed, deleted; project renamed. `project.Handler`
  owns the one `Dirty` path and the board handler signals through it
- **Reconnect resync** — `htmx:sseOpen` re-fetches the whole board, skipping the first
  open. Events sent while disconnected are gone and nothing replays them, so a board
  that merely resumed listening would sit quietly stale
- **`hx-trigger="sse:board-dirty, board-dirty"`** — the server's signal and the
  reconnect trigger converge on one re-fetch
- **`GET /debug/live`** — operator-only subscriber counts, refusing others with 404, so
  a hub leak is observable rather than invisible
- **`Hub.RoomCounts`** — a snapshot built under the read lock
- Hub tests for the leak (200 subscribe/cancel cycles with a goroutine count),
  cancel-after-close, an empty room, and a payload-free signal

**Gate — met.** `make check` and `make check-postgres` green, run three times for
flakiness. Walked against a running binary with two live `curl -N` streams:

| Check | Result |
|---|---|
| Two tabs watching one board | `rooms=1, total_subscribers=2` |
| Tab A moves a card | **A receives nothing; B receives `lane-updated`** |
| B's payload | two `hx-swap-oob` lane fragments, the card title present |
| **Delivery latency over ten moves** | **min 9ms, median 10ms, max 11ms — all under 100ms** |
| A lane added | **both** tabs receive `board-dirty` |
| Heartbeat | `: connected` then `: ping` comment frames |
| Closing tab A | subscribers 2 → 1 |
| Closing tab B | **rooms 0, subscribers 0** |
| SIGTERM with a stream open | the stream ends cleanly |
| Restart | subscribers reset to 0; the session survives; the board re-renders from the database |
| A fresh stream after the restart | receives `board-dirty` |
| A stream with no `?client=` | 400, nothing subscribed |
| A stream on a board with no grant | 404, nothing subscribed |
| 200 subscribe/cancel cycles | no goroutine growth, every room freed |

**Two test defects found, both mine.** An `httptest.ResponseRecorder` cannot be used to
test a stream — reading its `Body` while the handler writes is a data race, which passed
alone and failed under `-race`. And `TestSignInFailureIsGenericAndSetsNoCookie` ranged a
**map** and then compared the two responses positionally, so it had been passing by luck
since phase 2.

**Not verified.** Everything on this list is server-side. **The drag guard has still
never run in a browser** — `htmx:sseBeforeMessage` deferring a swap until the drop lands
is the one mechanism on the critical path that cannot be exercised without a pointer, and
it is what stops a card being swapped out from under the hand holding it.

---

## Phase 7 — Admin console

**Why here.** Requirements R3, R4, R5. It needs users, projects, and grants to
already exist, and it is the owner's surface rather than a member's.

**Build**

- `internal/admin/{domain,service,handler,store}`.
- `GET /admin` — a dashboard: users, projects, who has access to what.
- User create with a generated password **revealed exactly once**, never emailed,
  never logged, never stored in plaintext.
- Password reset with the same one-time reveal; sets `must_change_password` and
  **revokes every session that user holds**.
- Suspend and reinstate. A suspended user's sessions die immediately.
- Project create and archive; grant and revoke access; promote and demote a
  project manager.
- `web/pages/admin_users.templ`, `admin_projects.templ`.

**Gate.** The whole owner flow end to end: create an account, note the one-time
password, grant a project, watch that user log in and be forced to change the
password, reset it, watch their old session die, suspend them, watch them be
locked out mid-session. A generated password appears in no log line.

---

## Phase 8 — Harden and ship

**Why last.** Every item needs the thing it hardens to exist.

**Build**

- `internal/platform/backup` — a ticker running `VACUUM INTO` on
  `BACKUP_INTERVAL`, pruning past `BACKUP_KEEP`, plus `make backup`. Copying a
  live WAL database is not a backup.
- A session-reaper ticker deleting expired rows.
- 404 and 500 pages that look like the app.
- Empty states that say what to do, on the project list, an empty board, and an
  empty lane.
- Keyboard: every drag has a menu equivalent; lane reorder is available from the
  settings form; focus is visible and managed across every modal.
- Responsive pass at 390 / 768 / 1440 against the table in
  `../design/DESIGN-GUIDELINES.md`. Touch drag confirmed on a real phone.
- Re-measure contrast with `../design/contrast.py` and confirm the table.
- Shutdown drains open SSE streams.
- Records updated: `../status/STATUS.md`, `../memory/DECISIONS.md`,
  `../memory/LEARNINGS.md`, `../docs/`.

**Gate.** `make check` green. `docker compose up` from an empty volume yields a
working board reachable on the published port. The full manual walkthrough in
`../status/STATUS.md` passes. A restart loses nothing; a `make backup` snapshot
restores into a working database.

---

## Deliberately not in any phase

Named so they read as choices. Each is additive later.

Email · self-service signup · password-reset links · SSO · multi-tenancy beyond
project grants · card templates and recurring cards · full-text search (SQLite
FTS5, a small migration) · export and import · an audit log beyond
`card_activity` · two-factor authentication · public board sharing · card
dependencies, checklists, or time tracking · a mobile app.
