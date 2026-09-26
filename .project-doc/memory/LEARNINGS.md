# Learnings

Things discovered the hard way, or that a test caught before they became a
production bug. Short entries; the full explanation lives in
`../docs/PROJECT_KNOWLEDGE.md` under "Traps".

Append; do not rewrite history. If a learning turns out to be wrong, add a
correction below it rather than editing it away.

## 2026-09-26 — a non-blocking channel send still races a close

`sse.Hub.Broadcast` snapshotted its subscribers under a read lock, released it,
then sent. A concurrent `remove()` could close a channel between those two steps:
`panic: send on closed channel`.

Found by `TestConcurrentSubscribeBroadcastAndCancel` — 50 goroutines subscribing,
broadcasting, and cancelling under `-race`. It would not have shown up in ordinary
use for a long time, and then only as an occasional crash under load.

Fix: send **while holding the read lock**. That is safe precisely because the sends
cannot block (the `select` has a `default`), and holding the read lock excludes
`remove()`, which is the only thing that closes a channel. A subscriber present in
the map is therefore guaranteed not closed.

The general lesson: "I released the lock because sends can be slow" is wrong when
the sends are non-blocking. The lock was doing more work than it looked like.

## 2026-09-26 — htmx's SSE extension does not fire the swap events

`htmx:beforeSwap` never fires for a stream message. The extension calls htmx's
`swap()` directly instead of going through the request pipeline. The cancelable
event it does fire is `htmx:sseBeforeMessage`.

The first version of the drag guard hooked `htmx:beforeSwap` and therefore never
ran — a guard that silently does nothing, which is worse than no guard because it
looks present in the code.

Found by reading the vendored extension source (`web/static/vendor/htmx-ext-sse.js`,
the `swap()` function near the bottom) rather than by testing, because there was no
browser available. Reading the library was faster than guessing at it either way.

## 2026-09-26 — an out-of-band swap cannot move an element

`hx-swap-oob="outerHTML"` replaces an element **where it already sits**. It cannot
relocate a card into a different lane's container.

The plan called for surgical per-card fragments on a move. That is not
implementable without positional OOB targets
(`hx-swap-oob="beforebegin:#card-<next>"`) plus index arithmetic on a client whose
DOM may already have changed.

Broadcasting the affected **lanes** is correct by construction and costs a few
hundred bytes. This was a plan correction made during phase 0, not a bug found
later, which is exactly what the spike was for.

## 2026-09-26 — htmx applies OOB before honouring `hx-swap="none"`

Confirmed by reading the minified core: the swap function calls the OOB pass
(`_e`) before dispatching on `swapStyle`. So `hx-swap="none"` on the SSE host
element applies out-of-band content and touches nothing else.

This is the single assumption the whole live board rests on, which is why it was
verified rather than assumed. If it had been false, the fallback was a
`board-dirty` signal and a whole-board re-fetch — one attribute's difference.

## 2026-09-26 — Tailwind v4 cannot `@apply` a plain component class

`@layer components { .btn { @apply … } }` plus `.btn-primary { @apply btn … }`
fails the build with "Cannot apply unknown utility class `btn`". In v4, `@apply`
accepts only real utilities. Declaring it with `@utility btn { … }` registers it
and makes it applyable.

Cost: one failed build, resolved by reading the error rather than by guessing at
config, since v4 has no config file to guess at.

## 2026-09-26 — a runtime-assembled class name is invisible to Tailwind

Tailwind scans source text, so `"bg-lane-" + color` generates nothing. The failure
mode is an element that is silently unstyled — no error, no warning, nothing in the
build output.

`web/components/helpers.go` is therefore a switch returning whole literal class
strings. Every future helper of that kind must be too.

Related: v4's auto-detection was already scanning `helpers.go` even though only
`.templ` was in `@source`. Relying on that would have been fragile, so the
`@source` globs are now explicit about the `.go` files.

## 2026-09-26 — `grep -c` is the wrong tool on templ output

templ emits a whole page on one line, so `grep -c` reports 1 for a pattern that
occurs five times. Two verification passes read as "everything is missing" before
this was obvious. Use `grep -o … | wc -l`.

Separately: CSS selectors escape the colon, so `grep before:bg-danger` finds
nothing in built CSS while `before\:bg-danger` finds it. The first pass on the
stylesheet reported a false negative for the same reason.

## 2026-09-26 — an unanchored `vendor/` in `.gitignore` eats the front end

`vendor/` matches at any depth, so it silently excluded `web/static/vendor/` — the
pinned htmx, Alpine, and SortableJS files whose entire purpose is to be committed.
Caught by explicitly checking `git check-ignore -v` on each vendored file rather
than trusting the pattern list. It is `/vendor/`.

Worth generalising: after writing a `.gitignore`, check the files you *expect* to
be tracked, not only the ones you expect to be ignored.

## 2026-09-26 — the healthz wait loop raced `make build`

`make build && ./app & ; wait-for-health` backgrounds the whole `&&` list, so the
readiness loop ran while the build was still going, failed every attempt, and the
subsequent fetch wrote an empty file. Two minutes lost diagnosing a "broken page"
that was a timing artefact.

When scripting a smoke test, start the server in a step that has already finished
building.

## 2026-09-26 — modernc SQLite vs. the goose CLI

Installing the goose CLI with SQLite support means CGO or build-tag wrangling —
`go-chi-hex/Dockerfile` installs goose with `-tags 'postgres'` specifically to
*exclude* SQLite. Using goose as a library with `go:embed` avoids the problem
entirely and gives one migration code path for both boot and the command line.

Noticed while reading the existing Dockerfile in this workspace, before writing
anything. Reading the neighbouring project was cheaper than discovering it in a
Docker build.

## 2026-09-26 — modernc.org/sqlite does not understand a TIMESTAMPTZ column

Probed before writing any of the data layer, which is the only reason it did not
become a bug.

Writing a `time.Time` into a column declared `TIMESTAMPTZ` stores Go's
`time.String()` output — `2026-09-25 19:51:53.518063 +0000 UTC`. Reading it back
fails outright:

```
sql: Scan error on column index 0, name "created_at":
unsupported Scan, storing driver.Value type string into type *time.Time
```

`mattn/go-sqlite3` parses a handful of declared types into `time.Time`; modernc
does not. Worse than the failed read is the stored form: that string is not valid
PostgreSQL `timestamptz` input, so the data would not have been portable either.

Hence `database.Time`. The general lesson: the declared type in a SQLite column
is an affinity hint, and any driver behaviour that appears to depend on it must be
tested, not assumed.

## 2026-09-26 — a fixed-width timestamp is a correctness requirement, not tidiness

Once timestamps are text, `ORDER BY` is a string comparison. `time.RFC3339Nano`
trims trailing zeros from the fraction, and `.` (0x2E) sorts before `Z` (0x5A):

```
"2026-01-02T03:04:05.5Z"  <  "2026-01-02T03:04:05Z"    ← lexicographic
2026-01-02T03:04:05.5     >  2026-01-02T03:04:05       ← chronological
```

Rows come back in the wrong order, and nothing errors. `TestTimeStringSortMatches
ChronologicalSort` asserts the broken ordering RFC3339Nano produces, so the
reasoning cannot be quietly removed later.

## 2026-09-26 — sqlx does not know modernc's driver name

`sqlx.BindType("sqlite")` returns UNKNOWN; sqlx ships bind types for `"sqlite3"`
and `"nrsqlite3"` only. With UNKNOWN, `Rebind` returns the query unchanged — which
*works* for SQLite, because the placeholder already is `?`.

That is the trap: it works by accident, silently, and would break the moment the
driver became `pgx` and queries needed `$1`. `sqlx.BindDriver(DriverName,
sqlx.QUESTION)` in the package's `init` makes it intentional.

## 2026-09-26 — the two engines disagree on which constraint fires first

An insert naming a nonexistent project *and* a zero `wip_limit` reports:

- PostgreSQL → `23514` check_violation
- SQLite → `787` foreign-key violation

Both are correct; they evaluate in different orders. A test asserting a specific
error code must therefore violate exactly one constraint, or it is testing
evaluation order rather than the schema. Found when the PostgreSQL portability
test disagreed with the SQLite one over the same statement.

## 2026-09-26 — `url.Values.Encode` percent-encodes the pragma parentheses

The DSN is built with `url.Values`, so `_pragma=journal_mode(WAL)` goes on the
wire as `_pragma=journal_mode%28WAL%29`. modernc decodes it correctly, so this is
fine — but SQLite **silently ignores a pragma it cannot parse**, so "fine" was not
something to take on trust. `Open` now reads every pragma back on both pools and
fails startup if any did not take. A silently-off `foreign_keys` would make every
`REFERENCES` clause in the schema decoration.

## 2026-09-26 — `INTEGER PRIMARY KEY AUTOINCREMENT` has no portable equivalent

Checked properly before redesigning around it: SQLite auto-assigns only for
`INTEGER PRIMARY KEY`; PostgreSQL needs `BIGSERIAL`/`IDENTITY` and does not know
`AUTOINCREMENT`. `BIGSERIAL` in SQLite gets NUMERIC affinity and auto-assigns
nothing. There is no common syntax, so "one portable schema" and "an integer
surrogate key" cannot both hold.

Worth stating because it is the kind of thing that looks like it should have a
clever workaround and does not.

## 2026-09-26 — a named CHECK constraint is worth the extra words

SQLite reports the constraint *name* for a named CHECK and the whole *expression*
for an anonymous one:

```
CHECK constraint failed: users_role_check          ← CONSTRAINT users_role_check CHECK (…)
CHECK constraint failed: role IN ('super_admin',…) ← CHECK (…)
```

The name is what `database.Constraint(err)` can switch on to produce a useful
field error. PostgreSQL always reports the name, so naming them also makes the two
engines agree.

## 2026-09-26 — resetting the wrong rate-limit bucket on success is exploitable

A test asserted that a successful sign-in clears the throttle so a person who
eventually remembers their password is not still limited. It failed, because only
the per-account bucket is cleared and the per-IP one is not.

The test was wrong and the code was right, for a reason worth recording: if a
success cleared the address's bucket, an attacker holding one valid credential could
sign in whenever they approached the limit and carry on guessing at other accounts
indefinitely. The IP bucket has to survive a success.

That also exposed a real usability problem the original single-limiter design had —
one bucket for both keys means an office NAT throttles innocent users — which is why
there are now two limiters with different allowances.

## 2026-09-26 — identical error messages are not enough against enumeration

Argon2id is deliberately slow (~50ms). Returning early when no account matches
makes an unknown address measurably faster than a wrong password, and that timing
difference is a working enumeration oracle no matter how carefully the response
bodies are made to match.

Fix: verify against a decoy hash on the no-account path, so both do the same work.
Computed once at service construction, not per request.

## 2026-09-26 — a test double that erases a distinction hides bugs in its users

`password.Fake` originally returned `ErrMismatchedPassword` for everything that did
not match, including a string that was not a hash at all. That made
`TestAMalformedStoredHashIsAnInternalErrorNotAWrongPassword` unwritable: the fake
could not produce the case.

The real hasher returns `ErrInvalidHash` there, and the service depends on the
difference — a wrong password is 401, a corrupt stored hash is 500, because the
second is our bug and reporting it as the first would hide it forever while locking
the person out.

The general rule: a double must preserve every distinction its users branch on, not
merely the happy path.

## 2026-09-26 — `pkill -f` matches the shell running the script

`pkill -f 'bin/chonkboard'` killed the bash process too, because its command line
contained the pattern. The script died mid-verification with exit 144 and the
restart check never ran.

Capture the PID from `$!` and `kill` that instead.

## 2026-09-26 — templ's `attr?={ }` cannot omit an attribute that carries a value

That form takes a bool and toggles presence for *boolean* attributes. Passing it a
string gives "non-boolean condition in if statement" at compile time, and there is
no way to spell "omit aria-describedby entirely unless there is an error" with it.

Use a `templ.Attributes` spread from a plain `.go` helper in the same package. Also
worth knowing: HTML allows only one of a given attribute, so a markup-level
`aria-describedby` plus one from a spread silently loses whichever the browser sees
second — the helper has to own the whole attribute and take the hint ids as
arguments.

## 2026-09-26 — a helper that re-resolves access from the request breaks on routes without that parameter

`BoardShape.LaneSummaries` originally took `*http.Request` and called
`projects.Resolve(r)` to get a subject. That reads `chi.URLParam(r, "project")` —
which is empty on `GET /`, the project list.

Result: every board in the list came back as "no such project", so the list
rendered as a 404 toast. It would have hit the operator too, the moment there was
one project to list; the empty-list case hid it.

The fix is the shape of the port, not a special case inside it: pass the already
resolved subject in. The caller has always resolved access before asking, and a
port that re-derives its own authorisation context is a port that can be called
from somewhere the context does not exist.

## 2026-09-26 — `-race` plus `-coverpkg ./...` yields a zero-count profile

```
go test -coverprofile=c.out -coverpkg=./internal/...,./web/... ./...          # 64.2%
go test -race -coverprofile=c.out -coverpkg=./internal/...,./web/... ./...    #  0.0%
```

The second writes ~2200 lines of profile with every counter at zero, which reads
like a broken build rather than a toolchain wart. `make cover` therefore omits
`-race`; the race detector runs in `test` and `check`, and this target only measures.

Separately: `-coverpkg` belongs only on the aggregate profile. On a per-package
`go test -cover ./...` line it reports that binary's coverage of the *whole tree*,
so every package reads as 2-7% and the output says nothing.

## 2026-09-26 — Go's per-package coverage understates a handler tested through the router

`internal/board` reported 39.9% and `internal/project` 46.5% after this phase, which
looked like a regression. Measured with `-coverpkg`, the real figures are 97% and 92%
of functions.

Go credits cross-package coverage to the package the *test* lives in. The handlers
are driven through the assembled router from `httpserver`'s tests, so that is where
their coverage landed. Worth knowing before chasing a number that is an artifact.

## 2026-09-26 — a method value on a nil pointer registers fine and panics later

`chi.Get("/", d.Projects.List)` with `d.Projects == nil` is legal: the bound method
value is created without dereferencing. The panic arrives on the first request, as a
500 with a stack trace pointing at the handler body rather than at the wiring.

This happened twice in one phase, both times because a `python`-driven edit to the
`Deps` struct literal failed to match after gofmt realigned the field names, so the
new fields were silently never added. `mustHaveDeps` now panics at construction.

The editing lesson too: a replacement anchored on whitespace inside a struct literal
will stop matching the moment gofmt realigns it. Verify the edit landed, rather than
assuming a successful build means it did — a missing field in a literal still builds.

## 2026-09-26 — templ's `{ ... }` inside an `if` needs the whole expression, not a fragment

Two shapes that do not compile and whose errors do not say why:

`attr?={ someString }` — the conditional-attribute form takes a `bool` and toggles
presence for boolean attributes. Given a string it reports "non-boolean condition in
if statement", pointing at generated code. There is no way to spell "omit this
value-carrying attribute entirely" with it; use a `templ.Attributes` spread from a
plain `.go` helper in the same package.

And a helper used by a `.templ` must live in a `.go` file if it needs to import
`github.com/a-h/templ`: importing that package inside a `.templ` gives "templ
redeclared in this block", because the generator injects its own import.

## 2026-09-26 — a repeated form field with an empty value is a one-element slice

`from_order=` with nothing after it gives `r.PostForm["from_order"] == []string{""}`,
not `nil` and not `[]string{}`. Handed to the move planner, that empty string is a
card uuid that is not on the board, so the move is refused.

The case it breaks is dragging the last card out of a lane, whose source order is
genuinely empty. `board.js` appends `from_order` inside a loop over remaining cards
and so omits the key entirely — which is why the defect never showed through the
real client and appeared on the first `curl`.

Filter empties out of any repeated field whose elements are identifiers.

## 2026-09-26 — `AffectedLanes` cannot see a lane that lost its only card

It derives from the placements a plan produced, and a lane whose new order is empty
gets no placements. So the lane a card was dragged *out of* is missing from the list
whenever that lane is now empty — exactly the case where re-rendering it matters most,
because it has visibly changed.

The service knows the card's previous lane (it reads it for the activity row), so it
adds it. Worth remembering as a general shape: "what changed" derived purely from
"what was written" misses anything that changed by becoming absent.

## 2026-09-26 — removing a card leaves a hole in the position sequence

Delete and archive both take a card out of a lane without touching its siblings, so
five cards minus the middle one reads `0, 1, 3, 4`.

Nothing breaks — positions are only ever an ordering, and the next move renumbers the
whole lane anyway. But it makes the column's invariant conditional, and a conditional
invariant is one somebody will eventually rely on the wrong half of. `CompactLane`
runs in the same transaction as the removal.

## 2026-09-26 — a UUIDv7 prefix is not a usable short id in a debug script

Two lanes created in the same millisecond share their first 8 hex characters, because
v7 leads with a timestamp. A verification script that keyed a map on `uuid[:8]` merged
two lanes into one bucket and reported positions `[0, 1, 0]` as "not dense" — a
scripting artifact that looked exactly like a real ordering bug.

That leading timestamp is the whole reason for choosing v7 over v4. Print full uuids,
or key on something else.

## 2026-09-26 — `r.ParseForm` does not read a multipart body

It parses `application/x-www-form-urlencoded` only. On a file upload `r.PostForm` is
empty, so a CSRF check looking for a token field finds nothing and returns 403 — which
is how **every attachment upload in this application was broken** until the first
`curl -F` ran.

Invisible to a JavaScript client that sends the token as a header, and invisible to a
test that posts a URL-encoded form and calls it an upload. The route test now builds a
real `multipart.Writer` body for exactly this reason.

`r.ParseMultipartForm` is the fix, but it has to come after a body ceiling: it spools
past its memory limit to a temporary file, so without one an unbounded upload is written
to disk before anything can refuse it.

## 2026-09-26 — bluemonday strips `checked`, which silently destroys a checklist

`UGCPolicy` allows no attributes on `input`. A GFM task list then renders `[x]` and
`[ ]` identically — the markup is still there, the meaning is gone, and nothing errors.

Allowing it back needs a pattern that admits an *empty* value, because goldmark emits
boolean attributes as `checked=""` in XHTML mode and `SpaceSeparatedTokens` requires at
least one token. `type` is pinned to `^checkbox$` so no other input kind can appear.

The first version of the test asserted only that `disabled` was present, which would
have passed with the meaning still destroyed. The assertion that matters is that exactly
one of two items is ticked.

## 2026-09-26 — a port whose owner is a different slice should be its own port

`card.Handler` briefly had one `People` port with `DisplayName` and `Assignable`.
`*auth.Service` could not satisfy it, because who may be assigned a card depends on
project membership — auth's data does not include it.

Two ports, one per owner: `People` (auth) and `Assignees` (project). The compile error
was the design telling on itself.

## 2026-09-26 — a plain form post and an HTMX post want different answers

Comment, upload and archive all live inside the card modal, which is HTMX-driven in
practice and a plain form without it. A fragment is the right answer to the first and
useless as the second — there is no modal left to replace it in.

Every one of these handlers branches on `HX-Request`: fragment, or a redirect to the
board. Two route tests of mine asserted 200 for a plain post and were simply wrong about
which branch they were exercising.

## 2026-09-26 — an httptest.ResponseRecorder cannot be used to test a stream

Reading `rec.Body` while the handler still writes to it is a data race. The tests passed
run alone and failed under `-race` in `make check`, which is exactly the shape of bug
that gets dismissed as flakiness.

An SSE test needs `httptest.NewServer` and a real incremental reader — which is also
what the code under test actually faces, so the fix made the test more faithful as well
as correct.

## 2026-09-26 — a table-driven test over a map is order-dependent if the cases are compared

`TestSignInFailureIsGenericAndSetsNoCookie` collected two responses from a `map`
range and then compared `messages[0]` against `messages[1]`, normalising each against
a *different* hard-coded address. Map iteration order is random, so it passed or failed
by luck — and it had been in the suite since phase 2, passing every run until phase 6
happened to shuffle it.

Two fixes, both needed: a slice rather than a map when order matters, and normalise each
response against *its own* input rather than against a positional assumption.

## 2026-09-26 — `htmx.trigger` fires the bare event name, not the `sse:` one

`hx-trigger="sse:board-dirty"` listens for an SSE message. `htmx.trigger(el,
"board-dirty")` dispatches a plain custom event, which that trigger does not match — so
a reconnect handler written this way silently does nothing.

List both on the element. Caught by reading the two lines next to each other rather than
by a test, which is worth noting: nothing in the suite would have failed.
