# Status

Updated **2026-09-26**.

Phase definitions and gates: `../plan/ROADMAP.md`.

| # | Phase | State | Gate met |
|---|---|---|---|
| 0 | Scaffold and spike | **done** | yes, except one browser check — see below |
| 1 | Data layer | **done** | yes, including PostgreSQL portability |
| 2 | Auth and sessions | **done** | yes, walked end to end with curl |
| 3 | Projects, lanes, membership | **done** | yes, the member wall walked with curl |
| 4 | Cards core | **done** | yes, every move case walked with curl |
| 5 | Rich card | **next** | — |
| 6 | Live board | not started | — |
| 7 | Admin console | not started | — |
| 8 | Harden and ship | not started | — |

Repository: `/home/odin/repo/chonkboard`, its own git repo. **Nothing is committed
yet** — the working tree is complete and `.gitignore` is correct, but the first
commit was left to the owner.

---

## What a user can do today

**Run a board, short of putting cards on it.** All of this is real and persisted:

- First boot creates the operator's account and prints the password once.
- Signing in sets a hardened session cookie; every other route requires it. A
  handed-over password cannot be kept.
- `chonkboard seed user <email>` creates an account and prints its password once.
- The operator creates a project. It starts with five lanes — Backlog, In progress,
  Testing, Done, Stash — which a manager renames, recolours, reorders, gives a WIP
  limit, and deletes.
- The operator grants people access, and promotes somebody to manager. A manager
  adds and removes ordinary members.
- Everyone sees only the boards they were granted; the operator sees all.
- **A member sees the board and has no route that changes its shape.**

- Anyone granted a board **adds, edits, drags, archives and deletes cards.** A drag
  survives a reload. A WIP limit refuses an arrival with a toast and the board snaps
  back to what the database says. Each card keeps a history of when it was made,
  edited, archived and which lanes it moved between.
- A member may delete only **their own** card; a manager may delete any on their board.
- **Live updates work**: a move in one tab appears in every other tab on that board,
  without refreshing, and does not disturb the tab that made it.

What is missing from a card is its depth — priority, assignee, due date, labels,
comments and attachments. That is phase 5. Every one of those columns already exists
and round-trips; what is missing is the form fields and the panels.

---

## Phase 4 — done

### Built

- **`internal/card/service.go`** — create, edit, archive/restore, delete, move, and a
  card's history. Every method takes a subject; most are permitted to a member,
  because working on cards is what a member is here to do.
- **`internal/card/handler.go`** — the card routes, the board page and its fragment,
  the lane fragment, the SSE stream, and the broadcast after every mutation.
- **`internal/card/store.go`** — `CompactLane`, so removing a card leaves the lane's
  positions dense.
- **`web/components/{modal,card_form,card_modal}.templ`** — the modal shell with a
  real focus trap, the create/edit form, and the detail panel with its history.
- **`devboard.go` deleted.** Nothing from phase 0 remains on any route.

### Gate: met

`make check` and `make check-postgres` both green. The full verification table is in
`../plan/ROADMAP.md`. The rows that matter most:

| Check | Result |
|---|---|
| A drag across lanes, then a reload | survives |
| Reorder in place / across lanes / into an empty lane | all applied, positions dense |
| A WIP breach | 409, neither lane touched |
| A full lane reordered internally | allowed — the limit gates arrivals |
| A stale `from_lane` | 409 |
| A card or lane from another board | 404 |
| A move with no CSRF token | 403 |
| Delete the middle of five cards | lane renumbered 0-3 |
| **A member deleting somebody else's card** | **403** |
| An ungranted card, every verb | 404, never 403 |

### Three defects the walkthrough found

Worth recording because none would have been caught by the test suite as first
written, and two were invisible through the JavaScript client:

1. **Dragging the last card out of a lane was refused.** `from_order=` with an empty
   value arrives in Go as a slice holding one empty string, which the move planner
   rejects as a card that is not on the board. `board.js` omits the field entirely in
   that case, so only a hand-made request showed it.
2. **A lane that lost its only card was not re-rendered in other tabs.**
   `AffectedLanes` derives from placements, and an empty order produces none — so the
   lane that visibly changed was the one left out of the broadcast.
3. **Delete and archive left a hole in the position sequence.** Inert, but it made the
   column's invariant conditional.

### Live updates arrived early

The SSE hub, its heartbeat and the `ExceptClient` echo filter were built in phase 0.
Wiring the real card service into the existing broadcast was a few lines, and holding
it back for phase 6 would have been a regression. Phase 6 still owns the drag guard,
reconnect behaviour, and the `board-dirty` path for structural changes.

### Deliberately not done in this phase

Priority, assignee, due date, labels, comments and attachments — all phase 5. The
domain entity, the store and the card component already carry every one of them, so
`EditInput` grows rather than the plumbing. The edit path deliberately passes those
fields back unchanged, so editing a title cannot clear a due date set elsewhere.

---

## Phase 3 — done

### Built

- **`internal/project`** — `Resolve` turns a slug or uuid into a project plus the
  caller's grant plus an `authz.Subject`, in one place. Scoped `List`, `Create`
  (project + creator's grant + starting lanes, one transaction), rename, archive,
  restore, delete, and membership.
- **`internal/board`** — lane create, update, reorder, delete, labels, and
  `SeedDefaultLanes`. Every mutating method takes a subject and refuses a
  non-manager as its first statement.
- **`internal/auth/directory.go`** — the two read methods the project slice needs,
  on the auth service rather than its store.
- **`web`** — the project list, the settings page, the lane form, the delete-lane
  question, and the member row. All real pages at real URLs, all working with no
  JavaScript.
- **`cmd/chonkboard/seed.go`** and **`make seed-user`** — an account plus a
  one-time password. Not in the original phase list; without it there is no way to
  create a second account before phase 7, so the gate could not be exercised.
- **`httpserver.mustHaveDeps`** — the router refuses to start with an unwired
  handler, instead of panicking inside the first request.

### Gate: met

`make check` green. The full verification table is in `../plan/ROADMAP.md`. The rows
that matter most:

| Check | Result |
|---|---|
| A new project | five lanes, positions 0-4 |
| Rename, recolour, WIP limit, reorder, add, delete | all applied, positions stay dense |
| Delete down to the last lane | 409 — a board needs at least one |
| Delete a lane holding cards | refused without a target; cards appended with one |
| A member's board | 200, the same lanes |
| Lane controls rendered for a member | none |
| **Every board-shape route with a member's cookie and a valid CSRF token** | **403** |
| A member with no grant | no projects; the board is 404, not 403 |

**The 403s are authorisation, not CSRF.** The route test asserts a control request
first — one that reaches the handler and fails on its own merits — so a missing
token cannot make the wall appear to hold. This was a real risk: the first manual
run of this check used an empty token and every route returned 403 for the wrong
reason.

### Coverage note

`make check` prints per-package coverage, where `internal/board` reads 39.9% and
`internal/project` 46.5%. That is an artifact: Go credits cross-package coverage to
the package the *test* lives in, and both handlers are driven through the assembled
router from `httpserver`'s tests. `make cover` reports the honest aggregate — **board
97% and project 92% of functions**, 64.2% of statements overall.

### Deliberately not done in this phase

- **Labels have a service, a store and tests, but no UI.** They are only useful once
  a card can carry one, which is phase 5.
- **Lane drag-reorder is not wired.** The up/down buttons are the keyboard path, and
  a drag will post to the same `ReorderLanes` — so phase 6 adds the gesture without
  a new endpoint or a second reorder implementation.
- **The card routes are still phase-0 scaffolding.** `devboard.go` no longer backs
  the board page, but it still backs `/lanes/{l}/fragment` and
  `/cards/{c}/move`. Phase 4 replaces both and deletes it.

---

## Phase 2 — done

### Built

- **`internal/shared/password`** — Argon2id from `go-kit`, OWASP parameters,
  constant-time verify, plus a `Fake` that every other package's tests use because
  the real one costs 64MB and ~50ms per call.
- **`internal/shared/authz`** — the permission matrix, 100% covered. `Subject.Can`
  for ordinary actions and `CanOn` for the three where a member may act on their own
  work only.
- **`internal/shared/authctx`** — the user and session on the request context.
- **`internal/shared/ratelimit`** — token bucket, injectable clock, sweeper, 100%
  covered.
- **`internal/auth`** — service (login, authenticate, logout, change own password,
  bootstrap, session reaping), token minting, the session cookie, and the handler.
- **`internal/platform/middleware`** — `RequireSession`, `RequirePasswordChange`,
  `VerifyCSRF`, `LoginRateLimit`.
- **`web`** — a `Plain` layout with no board.js, the login page, the account page.
- **`cmd/chonkboard/serve.go`** — composition, first-boot bootstrap, and a
  housekeeping ticker that sweeps idle rate-limit buckets and expired session rows.

### Gate: met

`make check` green. The full verification table is in `../plan/ROADMAP.md`; the
checks worth repeating here:

| Check | Result |
|---|---|
| Wrong password vs unknown address | byte-identical responses; both 401 |
| Forged POST with no / wrong CSRF token | 403 |
| `must_change_password` | holds every route; `/account/password` and `/logout` stay reachable |
| Password change | other sessions revoked, this one kept |
| Suspend mid-session | next request refused, cookie cleared |
| 6th wrong sign-in | 429, and the correct password is also 429 |
| Stored data | Argon2id PHC hash, 64-hex token hashes, no plaintext |
| Restart | sessions survive |

### The permission matrix

`internal/shared/authz` is the single place any permission question resolves, and
its table test covers all 25 actions against five standings (operator, project
manager, project member, signed-in-with-no-grant, anonymous). The bold rows of the
matrix in `../docs/AUTH_FLOWS.md` are the brief verbatim — **a member creates,
edits and drags cards, and every board-setting action is refused to them** — and
that test is where it is enforced.

It is not yet reachable from a route, because there are no project routes until
phase 3. Wiring it in is the first thing phase 3 does.

### Deliberately not done in this phase

- **Admin user management** (`/admin/users`, password reset, suspend) is phase 7.
  The service methods it needs exist and are tested; the console does not.
- **`authz` is not yet called by any handler.** There is nothing to authorise until
  projects exist. The matrix is built and proven so phase 3 can use it rather than
  re-deriving rules inline.

---

## Phase 1 — done

### Built

- **`migrations/`** — five goose files, eleven tables, every one with a real
  `-- +goose Down`. Written against the DDL subset SQLite and PostgreSQL both
  accept.
- **`internal/platform/database/`** — two pools (writer pinned to one connection),
  pragma verification at open, the portable `Time`/`NullTime` type, `TxManager`
  with context-carried transactions, the shared query helpers, SQLite error
  mapping, goose-as-a-library, and a `dbtest` harness.
- **`internal/platform/config/`** — flat struct, `envDefault` everywhere,
  validation that names the offending variable.
- **`internal/shared/idgenerator/`** — UUIDv7 generation and validation.
- **Domain entities for all four slices** — `auth` (User, Session), `project`
  (Project, Membership, ProjectRole), `board` (Lane, Label, Colour, Reorder),
  `card` (Card, Priority).
- **A store per slice** — `auth`, `project`, `board`, `card`. The card store's
  `Move` runs the snapshot, `domain.Plan`, and every renumbered row in one
  transaction.
- **`chonkboard migrate up|down|reset|status|version|create`**, sharing one code
  path with boot.
- **Makefile** — `migrate-reset`, `check-postgres`.

### Gate: met

`make check` green — `gofmt`, `go vet`, `check-arch`, `go test -race`.

| Package | Coverage |
|---|---|
| `auth/domain` | 100% |
| `board/domain` | 100% |
| `project/domain` | 98.1% |
| `card/domain` | 97.2% |
| `platform/config` | 91.2% |
| `project` (store) | 87.6% |
| `shared/idgenerator` | 87.5% |
| `card` (store) | 85.9% |
| `auth` (store) | 83.9% |
| `board` (store) | 80.6% |
| `platform/database` | 63.8% |
| `shared/sse` | 38.9% |

**145 tests** in the default suite, plus **9** behind `-tags postgres`.

Verified against a real database file, not only in tests:

| Check | Result |
|---|---|
| `migrate up` from an empty file | version 5, eleven tables |
| `migrate reset` | every table gone — no `Down` is a stub |
| `migrate up` again | clean |
| `migrate down` then `status` | version 4, `00005` Pending |
| `migrate create "add card colours"` | `00006_add_card_colours.sql`, correct template |
| Bad configuration | refused at startup, naming the variable |
| Foreign-key violation actually raised | yes — proves the pragma is on |
| A statement matching no rows | not-found, never a silent success |
| 24 concurrent write transactions | all succeed; no `SQLITE_BUSY` |
| A read during an open write transaction | not blocked |

### PostgreSQL portability: verified

`make check-postgres` ran green against **PostgreSQL 17.11**. It applies the real
embedded migrations with goose's PostgreSQL dialect and asserts:

| Check | Result |
|---|---|
| Every migration applies | pass |
| Every rollback applies, no table left | pass |
| Columns get native `timestamptz` / `boolean` | pass |
| `database.Time`'s wire format is valid `timestamptz` input and reads back equal | pass |
| Nullable timestamps round-trip | pass |
| `ORDER BY created_at` is chronological | pass |
| Unique, CHECK and foreign-key constraints all fire | pass |
| `ON CONFLICT … DO UPDATE … excluded` upsert | pass |
| The partial index on live cards exists | pass |

This is the guard against drift: a migration that reaches for dialect-specific DDL
fails this suite rather than being discovered during a migration attempt.

### Deliberately not done in this phase

Stores for `comments` and `attachments`. The tables, constraints and cascades
exist and are exercised, but there is no service to use them until phase 5, and
writing speculative query shapes now would mean guessing. Named in the roadmap so
it is a decision rather than a gap.

---

## Phase 0 — done

Scaffold, tooling, front end, and the SSE + out-of-band-swap spike. Full list in
`../plan/ROADMAP.md`.

### Still outstanding: the browser check

**The drag gesture itself and the visual swap have not been exercised in a
browser.** No browser tooling was available in the sessions that built phases 0
and 1. Everything behind the gesture is proven over HTTP and, since phase 1, in
the database; the gesture is not.

This is the only outstanding item from phase 0 and should be closed before phase 4
builds more on the same path.

```bash
cd /home/odin/repo/chonkboard
make build && APP_ADDR=:8099 ./bin/chonkboard
```

Open `http://127.0.0.1:8099/` in two windows and confirm:

1. A card drags within a lane, and the new order survives a reload.
2. A card drags across lanes, and the move survives a reload.
3. The second window updates without a refresh, within about a second.
4. Dragging in one window is **not** interrupted when the other changes something
   mid-drag.
5. Dragging a fourth card into "In progress" (limit 3) shows a toast and the card
   snaps back.
6. On a narrow window the lane rail scrolls horizontally and a card still drags.
7. Tab to a card and press Enter — the card opens.

Record the outcome here when done.

### Scaffolding removed

`devboard.go` and the `Board` field on `httpserver.Deps` were deleted in phase 4.
Nothing from phase 0 remains on any route.

---

## Measurements

| | |
|---|---|
| Binary | ~14 MB (static, `CGO_ENABLED=0`, stripped) — grew with the SQLite driver |
| Stylesheet | **6.6 KB gzipped** |
| JavaScript | **52 KB gzipped**, four vendored libraries plus `board.js` |
| Migrations | 5 files, 237 lines of SQL |
| Go files | 86 hand-written |
| templ components | 18 |
| Tests | 292 default + 9 PostgreSQL-tagged |
| Aggregate coverage | 67.6% of statements |

---

## Phase 5 — next

The rich card: priority, assignee, due date, labels, comments, attachments, and the
detail modal that holds them.

Every column already exists and round-trips — phase 1 built and tested all of them,
and `web/view.Card` already renders a priority rule, a due date with its overdue
colouring, an assignee avatar and label chips. What is missing is the form fields,
the panels in the modal, and the comment and attachment stores.

Three things to be careful of, all already decided:

- `card.EditInput` grows; the service already passes the phase-5 fields through
  unchanged so that editing a title cannot clear a due date.
- Label management has a service, a store and tests from phase 3 but no UI. It gets
  one here, because a label is only useful once a card can carry it.
- An attachment is authorised per request against its card's project and returns
  **404** when not permitted, so a leaked URL does not confirm the file exists.

The gate is every rich field round-tripping, and the upload limits and attachment
authorisation enforced.
