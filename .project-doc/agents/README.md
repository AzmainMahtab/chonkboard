# Working in This Repository

Applies to any AI agent, harness, or person — Claude Code, opencode, Cursor,
Aider, Codex, Copilot, or a human with an editor. Nothing here requires a
particular tool.

## Start here, in order

1. `../../AGENTS.md` — the binding rules. Layer boundaries, the error model, the
   id split, the SQLite rules. This file wins over any code comment.
2. `../docs/PROJECT_KNOWLEDGE.md` — conventions and the traps that have already
   cost time. Read the traps even if your task looks unrelated.
3. `../status/STATUS.md` — what is actually built, and what is written but not yet
   verified. The difference matters.
4. `../plan/ROADMAP.md` — the phase you are in, and its gate.
5. `../memory/DECISIONS.md` — so you do not re-open a settled question.

There is no knowledge graph for this repository. It is small enough to read: 
`internal/` is five feature slices plus `platform/` and `shared/`, and every
slice has the same four parts.

## Order of work

Read the rules → read the slice you are changing → write the domain rule and its
test → wire outward through service, store, handler, template → run the gate →
update `../status/STATUS.md`.

Write **inside out**. A rule belongs in `domain/` with a table test before any
handler exists to call it. `internal/card/domain/move.go` is the model to copy:
it is pure, it has no database, and it holds every ordering rule in the product.

## The gate — run before claiming anything is done

```bash
make check      # gofmt + go vet + check-arch + go test -race
```

`make check-arch` fails if a `domain` package imports chi, sqlx, the SQLite
driver, templ, goose, `net/http`, or the env parser. That is not a style rule; it
is what keeps the rules testable without a server.

**If you touched `migrations/` or `internal/platform/database/`, also run:**

```bash
make check-postgres    # needs docker; takes seconds
```

It applies the real migrations to a throwaway PostgreSQL and round-trips the
types. It is the only thing that proves the schema is still portable, and
`make check` cannot tell you because it needs no server.

For anything touching the front end, also rebuild and look at it:

```bash
make build && APP_ADDR=:8099 ./bin/chonkboard
```

## Things that will waste your afternoon if you do not know them

These are the short version. The reasons are in `../docs/PROJECT_KNOWLEDGE.md`
under "Traps", and you should read that section in full at least once.

1. **`foreign_keys` is OFF by default in SQLite.** Every FK is decoration without
   the pragma. `Open` verifies it; do not remove that check.
2. **SQLite has one writer.** Go through `TxManager`, never `db.Writer()` /
   `db.Reader()` directly. Mixing them up gives you `SQLITE_BUSY` under load, not
   at your desk — and inside a transaction the read pool cannot see your own
   uncommitted rows.
3. **The driver will not store a `time.Time`.** `modernc.org/sqlite` does not
   understand a `TIMESTAMPTZ` column; a bare `time.Time` is stored as Go's
   `time.String()` and cannot be read back. Always `database.Time` /
   `database.NullTime`.
4. **The timestamp format is fixed-width on purpose.** SQLite compares these
   columns as text, and `time.RFC3339Nano` would sort `…:53.5Z` *before* `…:53Z`.
   Do not "simplify" `database.Layout`.
5. **There is no integer primary key.** `uuid TEXT PRIMARY KEY` is the only key,
   because `AUTOINCREMENT` has no PostgreSQL equivalent. Do not reintroduce one.
6. **The SQL must run on PostgreSQL.** Run `make check-postgres` after touching
   `migrations/`. It needs docker and takes seconds.
7. **Queries use named parameters** (`:uuid`), never `?` or `$1`. sqlx rebinds
   them per dialect. An `IN (…)` list goes through `database.SelectIn`.
8. **A statement that changed nothing is a not-found**, via
   `database.RequireRow`. Treating it as success loses the write silently.
9. **Scoping is authorisation.** `AND project_uuid = :project_uuid` on every
   mutation, so an id from another board matches nothing. Never filter for
   ownership afterwards.
10. **A class name assembled at runtime is invisible to Tailwind.** Helpers that
    pick a class must return the whole literal string.
11. **An out-of-band swap cannot relocate an element.** That is why live updates
    broadcast whole lanes, not single cards.
12. **`htmx:beforeSwap` never fires for an SSE message.** The cancelable hook is
    `htmx:sseBeforeMessage`.
13. **`templ generate` before `go build`,** or you are compiling against stale
    generated code. `make build` does both in order; a bare `go build` does not.
14. **Never `SELECT *`.** Name a column projection constant beside the model.
15. **A UI control that is hidden must also be refused at the route.** Assume a
    member will `curl` it — the route tests do.
16. **Never use the real password hasher outside `internal/shared/password`.** It
    costs 64MB and ~50ms per call. Use `password.NewFake()`.
17. **Identical error messages do not stop account enumeration.** The no-account
    login path verifies a decoy hash so the *timing* matches too. Do not
    "optimise" that early return back in.
18. **A successful sign-in resets only the account's rate-limit bucket,** never the
    address's — otherwise one valid credential clears an attacker's throttle
    forever.
19. **Permission questions go through `internal/shared/authz`.** Never re-derive a
    rule in a handler; a rule expressed twice eventually disagrees with itself.
    An unknown action is refused, so a new one needs a matrix row and a test.
20. **`templ`'s `attr?={ }` takes a bool** and cannot omit a value-carrying
    attribute. Use a `templ.Attributes` spread from a `.go` helper, and let it own
    the whole `aria-describedby` — HTML keeps only one of a duplicate attribute.
21. **Project access is resolved in one place:** `project.Service.Resolve`, which
    returns the project, the grant, and an `authz.Subject` together. Never resolve
    a project without also deriving what the caller may do to it.
22. **An ungranted board is 404, never 403.** A 403 confirms it exists, which
    enumerates every project slug in the installation.
23. **Board-shape methods check the subject themselves,** as their first statement,
    as well as in the handler. That is what keeps the wall standing when a new
    route forgets its guard.
24. **A port must not re-resolve its caller's context.** `LaneSummaries` taking a
    `*http.Request` and re-reading the `{project}` parameter broke the project
    list, where there is no such parameter.
25. **Per-package coverage understates the handlers.** They are tested through the
    assembled router, and Go credits that to `httpserver`. Use `make cover`.
26. **Card and lane fragment URLs carry no project.** `/cards/{c}` resolves its
    project *from the card*, then scopes to it. An ungranted card is 404, never 403.
27. **An in-lane reorder writes no activity row** — only a lane change does.
28. **Positions stay dense through delete and archive**, via `CompactLane`. The
    invariant is unconditional; keep it that way.
29. **Filter empties from repeated form fields of identifiers.** `from_order=` is a
    one-element slice holding `""`, which refuses the ordinary move of dragging the
    last card out of a lane.
30. **Card forms POST, never PATCH or DELETE.** A browser form can only issue GET or
    POST, and every form here must work with no JavaScript.
31. **A card body and a comment are rendered through `internal/shared/markdown`,**
    never passed to the browser raw and never rendered client-side. The CSP does not
    make injected markup safe — a form, an iframe or an `onerror` needs no script.
32. **`r.ParseForm` does not read a multipart body.** Use `ParseMultipartForm`, and
    only after `LimitBody` has capped the request.
33. **An attachment is authorised per request and refused with 404, never 403.** A 403
    confirms the file exists; the URL carries no project to make that harmless.
34. **Attachment bytes never live under the uploader's filename.** `filestore.NewName`
    generates one; the original is display and `Content-Disposition` only.
35. **Uploads are checked against an allowlist,** and every response carries
    `Content-Disposition: attachment` and `nosniff` regardless.
36. **The card form submits every field every time.** There is no partial update,
    because an absent field and a cleared field would be indistinguishable.
37. **A card change broadcasts lane fragments; a structural change broadcasts
    `board-dirty`.** Not a trade-off — an out-of-band swap cannot relocate an element,
    so an added or reordered lane has no fragment that expresses it.
38. **`lane-updated` carries `ExceptClient`; `board-dirty` does not.** The first would
    otherwise swap a card out from under the hand that dropped it; the second comes from
    a form post, and other tabs of the same person still need it.
39. **A reconnect re-fetches the whole board.** Nothing replays events missed while
    disconnected, so merely resuming the stream leaves it quietly stale.
40. **Never test a stream with an `httptest.ResponseRecorder`** — reading its Body while
    the handler writes is a data race. Use `httptest.NewServer`.
41. **Never write an inline event handler.** The CSP has no `'unsafe-inline'`, so
    `onsubmit=` and `onchange=` are blocked and silently never run. Put the behaviour in
    `board.js` behind a `data-` attribute.
42. **Every plain `<form method="post">` needs its own hidden `csrf_token`.**
    `hx-headers` on `<body>` covers HTMX requests only.
43. **Audit rendered output, not just responses.** Both bugs phase 7 found were in the
    HTML a browser receives, and every status-code test passed.
44. **The admin console answers 404 to anybody but the operator** — never 403, and never a
    redirect that confirms the route exists.
45. **A one-time password is rendered into the response that created it.** Never a
    redirect: a query string or a flash would persist it.
46. **`web/` must never import a `*/domain` package.** The CSRF token reaches a
    fragment through `view.CSRF`, not `authctx`, for exactly this reason. There is
    no gate enforcing it, so check with
    `go list -deps ./web/... | grep internal/.*/domain`.

## House rules

- Never add a comment that restates the code. Comment the *why*.
- Never create a README or documentation file unless asked. (The files in
  `.project-doc/` were asked for.)
- Never commit unless asked. Never commit on `main` — branch first.
- Preserve existing naming. If a slice does something one way, the next slice
  does it the same way.
