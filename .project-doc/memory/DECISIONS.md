# Decisions

Each entry: what was decided, and the reason that would otherwise be lost.

## 2026-09-26 — templ over html/template

HTMX's model is that the server returns HTML fragments. With templ a fragment is
a typed function, so returning one card or one lane is compile-checked and
refactorable. `html/template` would make every fragment a stringly-named runtime
lookup, and a typo a 500 rather than a build failure. Cost is a `templ generate`
step, which `make build` and air both own.

## 2026-09-26 — SortableJS for drag, not native HTML5 drag events

HTML5 drag events do not fire on touch devices. Native DnD would have made the
board desktop-only unless we hand-wrote touch fallbacks. Sortable is ~15KB
gzipped and brings cross-lane dragging, a drop placeholder, and auto-scroll.
Configured with `delay: 120, delayOnTouchOnly: true` so a swipe still scrolls the
lane rail on a phone instead of starting a drag.

## 2026-09-26 — dense integer positions, renumbered per move

Alternative was fractional ranking (LexoRank / midpoint floats), which makes a
move O(1) but needs a rebalancing job and can drift from what the user sees.
SortableJS already knows the resulting order of the container it dropped into, so
the client sends that order and the server writes `0..n-1` for the affected
lane(s) in one transaction. Deterministic, no drift, no rebalance. A lane with a
few hundred cards is a sub-millisecond write on a local file.

Consequence: `(lane_uuid, position)` must **not** be a unique index — the renumber
loop would trip it mid-transaction.

## 2026-09-26 — cookie sessions, not JWTs

`go-kit` uses ES256 JWTs with a Redis blacklist. For a server-rendered app that
is the wrong shape: a cookie is what a browser sends automatically, and
revocation becomes a row update instead of a distributed blacklist. Dropping JWTs
also drops Redis, which is most of why this app has no second container.

Only the SHA-256 of the session token is stored, so a database leak does not hand
over live sessions.

## 2026-09-26 — two SQLite connection pools

SQLite permits exactly one writer. A single pool with default `MaxOpenConns`
produces `SQLITE_BUSY` under concurrent writes; serialising through a writer pool
of one turns that error into a queue. A separate reader pool keeps reads
concurrent under WAL, which matters here because SSE holds connections open for
as long as a board is on screen.

## 2026-09-26 — goose as a library, not a CLI

Installing the goose CLI with a SQLite driver means either CGO or wrestling build
tags (`go-chi-hex/Dockerfile` installs it with `-tags 'postgres'` precisely to
exclude SQLite). Embedding the migrations with `go:embed` and calling `goose.Up`
on the app's own writer pool gives one code path for boot and for
`chonkboard migrate up`, and keeps the runtime image down to one binary.

## 2026-09-26 — pragmatic layered slices, not go-kit's CQRS

`go-kit/AGENTS.md` prescribes bounded contexts with CommandBus/QueryBus and an
event bus. For a single-purpose board that roughly doubles the file count without
buying anything: there is no second consumer of a card event, and no service to
extract. Kept from go-kit: consumer-declared ports, the single `AppError` model,
the uuid/rowid split, goose conventions, table-driven tests with in-memory fakes,
and the `check-arch` import gate. Dropped: the buses, the ceremony.

## 2026-09-26 — Tailwind standalone CLI, no package.json

Every other Tailwind setup in this workspace is npm + a bundler plugin, because
every other project already has Node. A Go binary does not, and adding Node purely
to build a stylesheet would put a second toolchain in the Dockerfile. The
standalone CLI is one downloaded binary, invoked from the Makefile and from a
dedicated Docker stage.

## 2026-09-26 — live updates broadcast whole lanes, not single cards

The plan called for surgical per-card fragments. That is wrong: an out-of-band
`outerHTML` swap replaces an element **where it already sits**, so it cannot move
a card from one lane's container into another's. Getting a cross-lane move right
that way would mean positional OOB targets (`beforebegin:#card-<next>`) and
arithmetic on the client. Re-rendering both affected lanes is correct by
construction and costs a few hundred bytes.

Structural changes (lane added, renamed, reordered, deleted) broadcast a
`board-dirty` signal instead and the client re-fetches the board.

## 2026-09-26 — the drag guard hooks htmx:sseBeforeMessage

`htmx:beforeSwap` never fires for a stream message: the SSE extension calls
htmx's `swap()` directly rather than going through the request pipeline. The
cancelable event it *does* fire is `htmx:sseBeforeMessage`, so that is where an
incoming update is held while a drag is in flight, and replayed on drop with
`htmx.swap(live, data, {swapStyle: "none"})`.

Verified by reading the vendored htmx source: the out-of-band pass runs **before**
the main swap, so `hx-swap="none"` still applies OOB content.

## 2026-09-26 — per-tab client id for echo suppression

Suppressing a broadcast by session id would break a user with two tabs open: both
share a session, so the second tab would go stale. `board.js` generates a
`crypto.randomUUID()` per tab, appends it to the SSE URL, and sends it as
`X-Client-Id` on mutations; the hub skips exactly that subscriber.

## 2026-09-26 — system font stack, no webfont

A tool that stays open for hours gains little from a display typeface and loses a
first paint to it. Zero bytes, no FOUT, native rendering. Revisit only if the
board grows a public-facing surface.

## 2026-09-26 — CSP allows 'unsafe-eval' for Alpine

Alpine compiles its inline expressions with `new Function`. The alternative is
Alpine's CSP build, which bans inline expressions in markup — a reasonable trade
later, but it changes every component's authoring style. Everything else is
same-origin: no CDN, no inline script, no external font.

## 2026-09-26 — the schema is written for PostgreSQL as well as SQLite

The owner's requirement: one body of SQL that later runs on PostgreSQL unchanged.
That is a real constraint on every DDL choice, and it overturned one decision
recorded above.

Enforced by `make check-postgres`, which applies this exact migration set to a
real PostgreSQL server and round-trips the types. It is a build-tagged test, so
`make check` does not need a server, but the claim is verified rather than
asserted — and a migration that drifts into dialect-specific DDL fails it.

Deliberately **not** portable, and expected to be rewritten:
`internal/platform/database/errors.go` (result codes differ), `dsn.go` (pragmas
are a SQLite concept), the two-pool arrangement, and one dialect string. Each is
named at the top of its file.

Alternative considered: two migration directories, one per dialect, which goose
supports natively. Rejected because it is duplication rather than portability —
the two would drift, and "the SQL is compatible" would stop being true the first
time someone edited one and not the other.

## 2026-09-26 — UUIDv7 as the sole primary key; no integer surrogate

**This reverses the id/uuid split** inherited from `go-kit/AGENTS.md` §5 and
recorded in the original plan.

`INTEGER PRIMARY KEY AUTOINCREMENT` is SQLite-only. PostgreSQL wants `BIGSERIAL`
or `IDENTITY`, and SQLite auto-assigns only on `INTEGER PRIMARY KEY`. There is no
syntax both accept, so the dual-id scheme and one portable schema are mutually
exclusive. Given portability was the stated requirement, the surrogate key went.

Cost: a 36-byte foreign key instead of 8, and larger key indexes. At a few
thousand cards that is immaterial.

Benefit beyond portability: it deletes the `INSERT … SELECT` parent-resolution
join from every store. A foreign key now references `uuid` directly, so a write
is a plain `INSERT` and "some parent did not exist" is still one round trip,
reported by the foreign-key violation.

UUIDv7 rather than v4 because these are now the actual keys: a v4 scatters every
insert across the B-tree, while v7 leads with a millisecond timestamp so
successive inserts land together. A test asserts generated ids sort in creation
order.

## 2026-09-26 — one fixed-width timestamp format, through a wrapper type

`database.Time` renders `2006-01-02T15:04:05.000000Z` and nothing else.

Two independent reasons, both discovered by probing the driver rather than by
reading about it:

`modernc.org/sqlite` does not recognise a `TIMESTAMPTZ` declared type. Handing it
a bare `time.Time` stores Go's `time.String()` output —
`2026-09-25 19:51:53.518063 +0000 UTC` — which fails to scan back and is not valid
PostgreSQL input either. So a wrapper was needed regardless of format.

Given a wrapper, the format has to be fixed-width, because SQLite compares these
columns as text and `ORDER BY created_at` is therefore lexicographic.
`time.RFC3339Nano` trims trailing zeros and `.` sorts before `Z`, so `…:53.5Z`
would sort *before* `…:53Z` — wrong order, no error. A test asserts RFC3339Nano
misorders, so the reason survives.

Six fractional digits because that is exactly PostgreSQL's `timestamptz`
precision, so the same strings cross without rounding. `Scan` also accepts a real
`time.Time`, which is what a PostgreSQL driver returns, so the models do not
change with the dialect.

## 2026-09-26 — named parameters everywhere, not positional placeholders

Store SQL is written with `:uuid`, never `?` or `$1`. `:uuid` is the same text on
both dialects; the placeholders are not. sqlx rebinds per driver.

sqlx does not know modernc's driver name (`"sqlite"`), so `BindType` reports
UNKNOWN and `Rebind` would hand the query back untouched — which happens to work
for SQLite, where the placeholder already is `?`. The package registers it
explicitly with `sqlx.BindDriver` precisely because that accident would stop
working the day the driver becomes `pgx`.

The one exception is an `IN (…)` list, which a named parameter cannot hold:
`database.SelectIn` expands with `sqlx.In` and then rebinds.

## 2026-09-26 — case-insensitive email by normalisation, not by collation

`COLLATE NOCASE` is SQLite-only; `CITEXT` is a PostgreSQL extension. So addresses
are folded in `domain.NormaliseEmail` on the way in, a plain `UNIQUE` index gives
the uniqueness, and `CHECK (email = lower(email))` makes the invariant the
database's rather than a convention someone can forget. Lookups normalise too, so
a caller that forgot still gets a hit.

## 2026-09-26 — a store that changes no rows returns not-found

Every mutating store method runs its row count through `database.RequireRow`.

An `UPDATE` or `DELETE` matching nothing means the row went away under the caller.
Treating that as success loses the write silently, which is the kind of bug that
surfaces days later as "it didn't save". Making it a 404 is both correct and
immediately visible.

This pairs with scoping: mutations carry `AND project_uuid = :project_uuid`, so a
card id from another board matches nothing and the row count turns that into a
refusal. **Scoping is the authorisation check**, not a filter applied afterwards.

## 2026-09-26 — TxManager.Reader returns the transaction when one is active

The read pool is a different connection and cannot see uncommitted rows. A store
reading around its own transaction would see stale data — a bug that appears only
once two writes are composed, which is exactly when it is hardest to spot. There
is a test asserting the transaction sees its own insert while the read pool does
not.

A nested `InTx` joins the transaction in flight rather than opening a second one:
SQLite has no nested transactions, and a second `Begin` on the one-connection
writer pool would deadlock waiting for itself.

## 2026-09-26 — the sign-in throttle is two limiters, not one

Per-IP and per-account are different risks and deserve different allowances.

An IP address is shared: a team behind one office NAT is many legitimate people, so
a tight per-IP bucket throttles innocent users. Its job is only to stop one host
hammering the form, so it is wide — 30 a minute.

An email address is one person, and that bucket is what actually bounds a guessing
attack on a given account. It is tight — 5 a minute, roughly 7200 a day against a
12-character minimum.

A successful sign-in resets **only** the account's bucket. Resetting the address's
bucket too would let an attacker who holds one valid credential clear their own
throttle at will and carry on guessing at everybody else's. Found by a test that
expected the wrong thing and turned out to be describing a real weakness.

## 2026-09-26 — an unknown address is verified against a decoy hash

`Service.LogIn` runs `hasher.Verify` against a throwaway hash when no account
matches, then returns the same error it returns for a wrong password.

Identical messages are not enough on their own. Argon2id is ~50ms, so returning
early for an unknown address would make it measurably faster than a wrong password
— and that difference is a working account-enumeration oracle regardless of what
the response body says. The decoy hash is computed once at construction rather than
per request, which is why `NewService` returns an error.

## 2026-09-26 — the session cookie belongs to the auth slice

`CookieName`, `SetSessionCookie` and `ClearSessionCookie` live in
`internal/auth/`, and `platform/middleware` imports them.

The first attempt had them in the middleware, which forced the handler either to
import the platform layer or to reach them through a package-level function
variable set at wiring time. Both were worse than the observation that the cookie
*is* the session, and the session is this slice's. The dependency runs one way:
middleware imports auth; nothing in auth imports middleware.

## 2026-09-26 — a password policy of length only

Minimum 12, maximum 256, no composition rules.

Composition rules ("one capital, one digit, one symbol") push people towards
predictable substitutions and away from length, which is the thing that actually
resists guessing. This follows current NIST guidance. The floor is 12 rather than
the more common 8 because there is no second factor and no email recovery here: the
password is the whole of the credential. The ceiling exists because Argon2id hashes
whatever it is given, and an unbounded field is a cheap way to make the server do
64MB of work per request.

## 2026-09-26 — the test hasher distinguishes a bad hash from a bad password

`password.Fake` returns `ErrInvalidHash` for a string that is not a fake hash, and
`ErrMismatchedPassword` only when the password genuinely differs.

The first version collapsed both into `ErrMismatchedPassword`, which made it
impossible to test that the auth service reports a wrong password as 401 and a
corrupt stored hash as 500. That distinction matters: reporting a corrupt hash as a
wrong password would hide the bug forever while locking the person out. A double
that erases a distinction the real thing makes will eventually hide a bug in the
code that depends on it.

## 2026-09-26 — the login page is not wrapped in the session middleware

Public routes (`/login`, `/static`, `/healthz`) sit in a separate chi group with no
session middleware, rather than inside one that skips them by path.

A path-based exemption list is a thing to get wrong: add a route, forget the list,
and it is either unreachable or unprotected. Two groups make the boundary
structural — everything in the authenticated group has a user on the context, and
that is visible in the router rather than asserted in a comment.

## 2026-09-26 — an HTMX request gets HX-Redirect, not a 303

The session middleware answers an expired HTMX request with `204` plus
`HX-Redirect: /login`.

A 303 would be followed transparently by the XMLHttpRequest and the whole login
page swapped into whatever element made the request — so a board would appear to
dissolve into a login form inside a lane. `HX-Redirect` tells htmx to navigate the
window instead.

## 2026-09-26 — the wall between a member and the board is checked twice

Every mutating method on `board.Service` and `project.Service` takes an
`authz.Subject` and refuses a non-manager as its first statement. The handlers check
too, before doing any work.

That is deliberate duplication. The handler check is what produces the 403 a person
sees; the service check is what makes the rule structural. A method that cannot be
called without presenting a subject cannot be called by a route that forgot to
guard itself — and phases 4 through 7 will add many routes to these services.

The cost is one branch per method. The alternative is a rule that holds only as long
as everybody remembers it, which for the requirement this project exists to satisfy
is not good enough.

## 2026-09-26 — an ungranted project is 404, never 403

`project.ErrNoSuchProject` covers both "there is no such board" and "you have no
grant on it", with the same message and the same status.

A 403 on a board somebody has no business knowing about confirms that it exists.
Given slugs are short and guessable, that is a slow enumeration of every project in
the installation. A 404 tells them nothing either way.

The same reasoning applies to `board.ErrNoSuchLane`, which covers a lane from
another board.

## 2026-09-26 — a project reference is a slug or a uuid, resolved in one place

`project.Service.Resolve(ctx, user, ref)` accepts either and returns an `Access`
holding the project, the caller's grant, and an `authz.Subject`.

Two forms because they have different jobs: a slug is what a person types, shares
and recognises in a URL; a uuid is what a fragment or an SSE payload carries, where
a rename must not break the reference. `idgenerator.Valid` decides which was given,
so a 36-character slug can never be misread as an id.

One place because the alternative is every route resolving a project *and*
separately deriving what the caller may do to it — two steps, of which the second
is the forgettable one.

## 2026-09-26 — a new project is created with its lanes, in one transaction

`project.Service.Create` writes the project, grants its creator manager, and calls
`board.Service.SeedDefaultLanes` — all inside one transaction, through a
`BoardSeeder` port the project slice declares.

A project whose creator cannot configure it, or one with no lanes, is immediately
broken. Making either a second step means a failure leaves a board somebody has to
go and repair by hand.

The five names are the ones the brief names — Backlog, In progress, Testing, Done,
Stash — and are a starting point, not a fixture: a manager renames, recolours,
reorders and deletes them.

## 2026-09-26 — lane reorder is one whole-board order, not a swap

`ReorderLanes` takes every lane on the board and renumbers all of them, refusing an
order that does not name them all.

Same discipline as card moves, for the same reason: a partial order leaves the
unnamed lanes holding positions that collide with the new ones. The up/down buttons
build the complete order server-side and hand it to the same method a drag will use
later, so there is one reorder implementation rather than two that can disagree.

## 2026-09-26 — a board keeps its last lane, and a lane keeps its cards

Two refusals that exist to stop work disappearing:

`DeleteLane` refuses when it would leave zero lanes. A board with no lanes has
nowhere for a card to exist, and the state is only reachable by deleting down to it.

Deleting a lane that holds cards requires a `move_to` lane. The foreign key is
`ON DELETE RESTRICT` precisely so the database refuses too; the service turns that
into a question with a `<select>` of the other lanes rather than a 500.

## 2026-09-26 — board settings are pages, not modals

Adding and editing a lane, and confirming a delete, are full pages at real URLs
rather than HTMX fragments swapped into `#modal`.

A lane form is five fields and a colour palette, and the delete confirmation is a
genuine decision about where cards go. Both deserve something linkable and
bookmarkable that submits with no JavaScript. The same templ components drop into a
modal later without changing a route, so this is not a decision that has to be
unwound to add one.

The lane reorder buttons are the same reasoning applied to a gesture: dragging is a
pointer gesture, so a board whose lanes can *only* be dragged is unusable without a
mouse.

## 2026-09-26 — the UI layer carries the CSRF token on its own context key

`view.WithCSRF` / `view.CSRF`, populated by the session middleware, rather than
components reading `shared/authctx`.

A fragment renders without a `view.Page`, so it cannot take the token as a prop the
way a full page does, and threading it through every component signature would put
it in dozens of places that do not otherwise care. But `authctx` exposes the auth
domain's `User`, and a component that *can* reach a domain type eventually will —
which is the thing `web/view` exists to prevent. The carrier in `view` imports
nothing but the standard library.

## 2026-09-26 — the router refuses to start with an unwired dependency

`httpserver.Router` calls `mustHaveDeps` and panics on a nil handler.

A method value on a nil pointer is legal Go, so `d.Projects.List` registers fine and
panics later, inside a request, as a 500 with a stack trace. That is exactly how a
missed field in the `Deps` literal was found during this phase — twice, because the
struct literal was realigned between edits. Failing at construction turns a
confusing runtime 500 into an immediate, named boot failure.

## 2026-09-26 — card URLs carry no project, so the project is resolved from the card

`/cards/{c}` and `/lanes/{l}/fragment`, not `/projects/{p}/cards/{c}`.

These appear in the DOM on every card and in the move request `board.js` builds, so
the short form is worth having. The cost is that access cannot be resolved from the
path: `card.Service.ProjectOf` and `board.Service.ProjectOfLane` are deliberately
unscoped lookups that yield only a project uuid, the handler resolves access against
that, and then re-reads the thing scoped to it.

Two reads, and the safety is identical: an ungranted card is a **404**, the same as
one that does not exist. The handler maps `project.ErrNoSuchProject` onto
`card.ErrNoSuchCard` so the response cannot distinguish "not yours" from "not there".

A consequence worth knowing: a move names its card in the URL, so the *card* decides
which board is being written. Moving a foreign card therefore fails on the lane
(`ErrLaneNotOnBoard`) rather than on the card. Both are 404.

## 2026-09-26 — the board page lives in the card slice

`BoardPage` and `BoardFragment` are `card.Handler` methods, not `board.Handler` ones.

The page renders cards, and the card mapping belongs to this slice. Putting it in
board would need a second cross-slice port plus a duplicate of `toCardViews`, whereas
the card handler already holds both halves — it depends on board for lanes.

Board keeps what it owns: lane and label management.

## 2026-09-26 — an in-lane reorder writes no activity row

`Move` records a `card_activity` entry only when the card changed lane.

A reorder inside one lane happens constantly — it is the most common gesture on the
board — and logging it would bury the interesting history of a card under noise. What
a person wants from that log is "when did this reach Testing", not "it was nudged up
one at 14:32".

## 2026-09-26 — a lane is renumbered after a delete or an archive

`Store.CompactLane` runs inside the same transaction as the removal.

Removing a card leaves a hole in the sequence. Nothing reads positions as anything
but an ordering, so a gap is functionally inert — but the invariant for this column
is "dense and zero-based", and an invariant that holds *except* after a delete is a
worse thing to have to remember than one that always holds. Found by walking the
gate: after deleting the middle of five cards the lane read `0, 1, 3, 4`.

## 2026-09-26 — a lane the card left is affected even with nothing to renumber

`Service.Move` adds the previous lane to the affected list when the card changed lane
and the planner wrote no placement for it.

`AffectedLanes` derives from placements, and dragging the *last* card out of a lane
leaves an empty source order — so the plan renumbers nothing there and the lane
appears unaffected. It is not: it is now empty, and a tab that is not told would go
on showing the card in it. Caught by a service test asserting both lanes came back.

## 2026-09-26 — an empty repeated form field is not a one-element order

`orderField` drops empty strings from `to_order` and `from_order`.

A form that submits `from_order=` with no value arrives in Go as a slice holding one
empty string, which the planner rejects as a card that is not on the board. That
refuses the ordinary move of dragging the last card out of a lane. `board.js` omits
the field entirely in that case, so the bug was invisible through the JavaScript
client and appeared immediately in a hand-made request.

## 2026-09-26 — the create form takes a description, in two writes

`Create` takes a title only; the handler follows it with an `Edit` when a description
was given.

The create form offers both fields because asking somebody to save twice to write a
sentence is hostile. Keeping `Create` to a title keeps the "new card" concept small,
and the second write is in the same request. If this ever needs to be one
transaction, the service is where that changes — not the form.

## 2026-09-26 — markdown is rendered server-side and sanitised, with raw HTML escaped

goldmark without `WithUnsafe`, then bluemonday's `UGCPolicy`. Two lines of defence,
and the first one means raw HTML in a card body is escaped before the sanitiser even
sees it.

**The CSP does not cover this.** It blocks script; injected markup needs none — a form
posting to another origin, an iframe, an `onerror` on an image, a `javascript:` href, a
fixed-position element covering the page. Every one of those is a way to do damage with
no `<script>` tag, and every one is in the test.

Rendering client-side would be worse: the sanitiser would be the client's, and a client
that skipped it would render whatever was stored.

Links carry `rel="nofollow noopener noreferrer"` and `target="_blank"`, so an opened
page cannot reach back through `window.opener`.

## 2026-09-26 — attachments live on disk, addressed by a generated name

`internal/platform/filestore`, not a BLOB column, and never under the uploader's
filename.

Not in the database, because a 10MB blob in a row makes every query that touches the
table drag it through memory, makes `VACUUM INTO` backups proportional to total upload
size rather than to the board, and gives up being served by a syscall.

Not under the uploader's name, because a user-supplied filename reaching the filesystem
is a path-traversal bug (`../../etc/passwd`), a collision between two people uploading
`report.pdf`, and on a case-insensitive filesystem a way to overwrite somebody else's
file. The original is kept in the database and used only in a `Content-Disposition`
header. `pathFor` re-validates the name anyway — it is the last line before a path
reaches the filesystem, and "it cannot happen" is how traversal bugs get written.

Files are fanned out into 256 subdirectories by their first byte: one directory with
tens of thousands of entries is slow to list and, on some filesystems, slow to open a
file in.

## 2026-09-26 — the upload type check is an allowlist

A denylist is a guess at what is dangerous, and it is always incomplete. Everything on
the list is something a browser renders inertly or offers to download; `text/html` and
`image/svg+xml` are deliberately absent, because both execute script.

Every response carries `Content-Disposition: attachment` **and** `nosniff` regardless,
so even a file whose declared type is wrong cannot execute in this origin. The
allowlist is the first line, not the only one.

The claimed type is normalised (parameters dropped, folded to lower case) and falls
back to the extension when a browser sends `application/octet-stream`, which it does
for anything it does not recognise.

## 2026-09-26 — a leaked attachment URL is not a leaked file

`/attachments/{x}` is authorised per request against the card's project, and **every**
refusal is `ErrNoSuchAttachment` — a 404.

A 403 would confirm the file exists. The URL carries no project, so there is nothing to
make that harmless. The route test asserts the response to a leaked URL is
**byte-identical** to the response to a made-up id, because a difference in length or
wording is an oracle just as much as a difference in status.

The same reasoning covers a row whose bytes have gone missing: that is our bug, but
there is nothing the person can do, so it reports as missing rather than as a 500.

## 2026-09-26 — the CSRF check parses a multipart body

`r.ParseForm` reads only `application/x-www-form-urlencoded`. On a file upload it finds
no fields at all, so the token looked absent and **every attachment upload was a 403**.
The upload form is a plain `<form>` with no JavaScript, so a field is its only way to
present a token.

Fixing it needed a ceiling to exist first: parsing a multipart body in middleware spools
it to disk, so an unbounded upload would be written before anything could refuse it.
Hence `middleware.LimitBody`, applied globally before anything parses a body, at
`UPLOAD_MAX_BYTES` plus a megabyte of envelope slack. That was planned for phase 8; it
had to come now.

## 2026-09-26 — a deleted comment leaves a tombstone

`comments.deleted_at`, not a `DELETE`.

A thread that silently loses a message leaves the replies above it making no sense. The
row stays and renders as "X removed a comment"; it stops counting towards the card's
badge. Deleting twice is not an error, because a double-submitted form must not show a
failure for something that already happened.

## 2026-09-26 — the card form submits every field, every time

There is no partial update. An absent field and a cleared field would otherwise be
indistinguishable, which makes "clear the due date" inexpressible.

The consequence is that `EditInput` carries `nil` for "nobody" and "no due date", and an
empty `LabelUUIDs` clears every label. All of it lands in one transaction, so a card is
never observed half-saved. A separate endpoint per field would have avoided the
ambiguity and bought three more round trips and a half-saved state.

## 2026-09-26 — an assignee must have access to the board

`card.Service` asks `project.Service.CanSee` before writing one.

The foreign key already refuses an unknown uuid. This catches a *known* person with no
grant, who would otherwise be assigned work they cannot open. It is a second
consumer-declared port (`Members`) rather than a method on the existing `People` port,
because membership is the project slice's data and names are auth's — one port per
owner.

## 2026-09-26 — "Move to lane…" builds the order server-side

The keyboard path for a drag posts only a target lane; the service reads both lanes and
derives the two orders itself, then calls the same `Move` the drag uses.

A menu has no idea what else is in the target lane, so asking the client for an order
would mean inventing one. Going through `Move` means one implementation of the ordering
rules, one transaction, and the same WIP check — verified by a test that trips the limit
through this path.

## 2026-09-26 — a filename in a header is sanitised, and carried twice

`Content-Disposition` gets an ASCII-only quoted `filename=` with quotes, separators,
control characters and non-ASCII stripped, plus `filename*=UTF-8''…` per RFC 5987.

A filename is the one piece of user input that reaches a response header. A quote closes
the quoted string early; a CR or LF ends the header and starts another. Both are header
injection. The `filename*` form carries the real name for a browser that understands it,
while the quoted form stays a safe fallback for one that does not.

## 2026-09-26 — a structural change broadcasts a signal, not a fragment

`sse.Event{Name: "board-dirty"}` with no payload; the client re-fetches the whole
board.

Not an optimisation trade-off — an out-of-band swap **cannot relocate an element**. A
lane that was added, or reordered, or deleted, has no existing element to replace, so
there is no fragment that expresses the change. A whole-board re-fetch is the only
correct answer, and it is rare enough that making the common case surgical and this
case whole is the right split.

No `ExceptClient` on it. A structural change arrives from a full-page form post, not a
fetch carrying `X-Client-Id`, and the acting tab has been redirected to the settings
page anyway — while any *other* tab that person has open on the board needs the signal
as much as anybody else's.

Renaming a project counts: its name is in the board header.

## 2026-09-26 — the board listens for two triggers that mean the same thing

`hx-trigger="sse:board-dirty, board-dirty"` on one element with one `hx-get`.

`sse:board-dirty` is the server's signal. The bare `board-dirty` is what `board.js`
fires after a reconnect. Two names, one element, one re-fetch — rather than a second
code path that can drift from the first. `htmx.trigger` fires the plain event, which is
why both are needed.

## 2026-09-26 — the client re-fetches the whole board on reconnect

`htmx:sseOpen`, skipping the first open.

EventSource retries on its own, but events sent while it was disconnected are simply
gone: nothing replays them. A board that merely resumed listening would sit quietly
stale, which is worse than visibly broken because nobody notices. The first open is
skipped because the page was just rendered from the database — there is no gap to close,
and fetching there would double every page load.

Two flags rather than one: `streamUp` (is it up now) and `awaitingFirstOpen` (was this
the initial connection). Named `streamUp` rather than `live` because two functions in the
same file take a local `live` for the `#live` element, and a shadowed flag is a bug
waiting for the next edit.

## 2026-09-26 — subscriber counts are an operator-only route, not on /healthz

`GET /debug/live`, refusing a non-operator with **404** rather than 403.

A leak in the hub is otherwise invisible: a subscriber that was never removed costs one
goroutine and one buffered channel, appears nowhere, and only becomes obvious once there
are thousands. Making the number queryable is the whole point.

Not on `/healthz`, because room counts say how many projects are in use and how many
people are watching them — not something to publish unauthenticated. 404 rather than 403
because a debug surface should not advertise itself.

`Hub.RoomCounts` returns a snapshot built under the read lock, so a caller cannot hold
the lock while it renders.
