# Route surface

Every route the application serves. Declared in
`internal/platform/httpserver/routes.go` — a new top-level prefix goes there, not
in `main.go`, so one file answers "what does this app serve?".

A route returns one of three things:

- a **full page** (templ `pages.*`), for a browser navigation
- a **fragment** (templ `components.*`), for an HTMX swap
- **204 with no body**, when the result reaches the client over SSE instead

`{p}` is a project uuid, `{c}` a card uuid, `{l}` a lane uuid, `{u}` a user uuid,
`{x}` a comment or attachment uuid. Every one is a UUIDv7; an internal integer id
never appears in a URL.

## Public — **built**

These sit in their own chi group with **no session middleware**, rather than inside
one that skips them by path. A path-based exemption list is a thing to get wrong —
add a route, forget the list, and it is either unreachable or unprotected — so the
two groups make the boundary structural instead.

| Method | Path | Returns | Notes |
|---|---|---|---|
| GET | `/login` | page | redirects to `/` when already signed in; no board.js |
| POST | `/login` | 303, or the form with errors | `LoginRateLimit`: 30/min per IP, 5/min per account |
| GET | `/healthz` | `text/plain` | liveness; also the Docker healthcheck |
| GET | `/static/*` | asset | embedded, `immutable`, content-hashed query, directory listings 404 |

## Signed in, outside the password gate — **built**

`POST /logout` is not public: it needs a session to revoke. It sits above the
forced-password-change gate so a user held on that page can still leave, which would
otherwise be a redirect loop with no way out.

| Method | Path | Returns | Notes |
|---|---|---|---|
| POST | `/logout` | 303 `/login` | revokes the session row, clears the cookie |

## Signed in, password gate applied — **built**

| Method | Path | Returns | Notes |
|---|---|---|---|
| GET | `/account` | page | the change-password form |
| GET | `/account/password` | page | same page; where `must_change_password` holds a user |
| POST | `/account/password` | page | revokes every *other* session on success |

## Any signed-in user — **built**

`{p}` accepts a slug or a uuid. A slug is what a person types and shares; a uuid is
what fragments and SSE payloads carry, where a rename must not break the reference.
`project.Service.Resolve` decides which was given and refuses with **404** — never
403 — when the caller has no grant, because a 403 would confirm the board exists.

| Method | Path | Returns | Notes |
|---|---|---|---|
| GET | `/` | page | project list, scoped to the caller's grants; `?archived=1` to include archived |
| GET | `/projects/{p}` | page | the board — real lanes from the database |
| GET | `/projects/{p}/board` | fragment | whole board; reconnect and `board-dirty` target |
| GET | `/account` | page | change your own password |
| POST | `/account/password` | page | revokes every *other* session |

## Cards — **built**

Everything a granted member is here to do. These are **POST, not PATCH or DELETE**:
the forms are real `<form>` elements that work with no JavaScript, and a browser form
can only issue GET or POST.

`/cards/{c}` and `/lanes/{l}/fragment` carry **no project**, because they appear in the
DOM on every card and in the move request `board.js` builds. Each handler looks the
project up from the card or lane, resolves access against it, and then re-reads the
thing scoped — so an ungranted card is a **404**, indistinguishable from one that does
not exist.

| Method | Path | Returns | Notes |
|---|---|---|---|
| GET | `/projects/{p}/lanes/{l}/cards/new` | fragment | the create form, into `#modal` |
| POST | `/projects/{p}/lanes/{l}/cards` | fragment | create; returns the lane |
| GET | `/lanes/{l}/fragment` | fragment | one lane; the revert target after a failed drag |
| GET | `/cards/{c}` | fragment | detail, into `#modal` |
| GET | `/cards/{c}/edit` | fragment | the edit form |
| POST | `/cards/{c}` | fragment | save; returns the lane |
| POST | `/cards/{c}/archive` | 303 or fragment | `archived=1` or `0` |
| POST | `/cards/{c}/delete` | 303 or fragment | **own card only, for a member** |
| POST | `/cards/{c}/move` | **204** | the drag; broadcasts over SSE |
| GET | `/projects/{p}/events` | `text/event-stream` | SSE; **requires `?client=<tab id>`** — without one there is no way to skip the originating tab |

Archive and delete answer an HTMX request with the lane fragment and an
`HX-Trigger: card-gone`, and a plain form post with a redirect to the board — there is
nothing for a fragment to replace when the modal that held the button is gone.

## Rich card — **built**

| Method | Path | Returns | Notes |
|---|---|---|---|
| POST | `/cards/{c}/move-to` | 303 or fragment | the keyboard path for a drag; the server derives both lane orders |
| POST | `/cards/{c}/comments` | 303 or fragment | markdown, rendered and sanitised on render |
| POST | `/comments/{x}/delete` | 303 or fragment | soft delete; **own comment only, for a member** |
| POST | `/cards/{c}/attachments` | 303 or fragment | multipart; size and MIME limited |
| GET | `/attachments/{x}` | file | **authorised per request; 404 when not permitted** |
| POST | `/attachments/{x}/delete` | 303 or fragment | **own attachment only, for a member** |

Labels are attached by submitting the card form — there is no separate attach endpoint,
because a label is one of the fields the form already writes in a single transaction.

**`GET /attachments/{x}` never returns 403.** A 403 would confirm the file exists, and
the URL carries no project to make that harmless. The route test asserts the response to
a leaked URL is byte-identical to the response for a made-up id.

Every response carries `Content-Disposition: attachment` and `X-Content-Type-Options:
nosniff`, so even a file whose declared type is wrong cannot execute in this origin. The
filename in the header is ASCII-sanitised and repeated as `filename*=UTF-8''…`.

## Project label management — **built**

| Method | Path | Returns | Notes |
|---|---|---|---|
| POST | `/projects/{p}/labels` | 303 | manager or above |
| GET | `/projects/{p}/labels/{l}/edit` | page | the settings page with the form filled in |
| POST | `/projects/{p}/labels/{l}` | 303 | |
| POST | `/projects/{p}/labels/{l}/delete` | 303 | assignments to cards cascade |

## Project manager or above — **built**

Every route here is behind a manager check **twice**: the handler produces the 403,
and the service refuses again as its first statement. The duplication is deliberate —
the service check is what makes the rule survive a route added later without its own
guard, and phases 4–7 add many.

A member reaching any of these gets **403**, whether or not the UI offered it. The
route test asserts this with a valid CSRF token, and asserts a control request first,
so a 403 cannot be the CSRF check passing for the wrong reason.

| Method | Path | Returns | Notes |
|---|---|---|---|
| GET | `/projects/{p}/settings` | page | lanes, people, details, danger zone |
| POST | `/projects/{p}/settings` | 303 | name and description |
| GET | `/projects/{p}/lanes/new` | page | the create form |
| POST | `/projects/{p}/lanes` | 303 | appended at the end |
| GET | `/projects/{p}/lanes/{l}/edit` | page | the edit form |
| POST | `/projects/{p}/lanes/{l}` | 303 | name, colour, WIP limit, done flag — never the position |
| POST | `/projects/{p}/lanes/{l}/move` | 303 | one place up or down; the keyboard path |
| POST | `/projects/{p}/lanes/reorder` | 204 | a whole new order; must name every lane |
| GET | `/projects/{p}/lanes/{l}/delete` | page | asks where the cards go |
| POST | `/projects/{p}/lanes/{l}/delete` | 303 | requires `move_to` when the lane holds cards |
| POST | `/projects/{p}/members` | 303 | `role=manager` needs the operator |
| POST | `/projects/{p}/members/{u}` | 303 | re-role; the operator alone |
| POST | `/projects/{p}/members/{u}/revoke` | 303 | a manager may remove ordinary members only |

Refusals worth knowing: deleting the last lane is **409**, so is demoting or removing
the last manager, and so is deleting a lane with cards and no `move_to`. Revoking
somebody who is not a member is **404**.

## Operator only — **built**

| Method | Path | Returns | Notes |
|---|---|---|---|
| GET | `/debug/live` | JSON | SSE subscriber counts per board; **404 to anybody else** |
| GET | `/projects/new` | page | the create form |
| POST | `/projects` | 303 | project + creator's manager grant + five lanes, one transaction |
| POST | `/projects/{p}/archive` | 303 | toggles; archived boards leave every list |
| POST | `/projects/{p}/delete` | 303 | cascades to lanes, cards, labels, grants, activity |

## Project label management — *phase 5*

The service, store and tests exist; there is no UI until a card can carry a label.

| Method | Path | Returns |
|---|---|---|
| POST | `/projects/{p}/labels` | fragment |
| PATCH | `/labels/{l}` | fragment |
| DELETE | `/labels/{l}` | 204 |

## The admin console — **built**

**Every route here answers 404 to anybody but the operator** — never 403, and never a
redirect. `GET /admin` checks before it redirects for exactly that reason: a bare redirect
confirms the surface exists, which is what the policy is for.

Project create, archive and delete live on the project routes above rather than being
duplicated here; `/admin/projects` links to them.

| Method | Path | Returns | Notes |
|---|---|---|---|
| GET | `/admin` | 302 `/admin/users` | checks first |
| GET | `/admin/users` | page | every account, with live sessions, boards, and pending handovers |
| POST | `/admin/users` | page | **reveals the generated password exactly once** |
| GET | `/admin/users/{u}/edit` | page | the list, with that account in the form |
| POST | `/admin/users/{u}` | 303 | name, address, and the global role |
| POST | `/admin/users/{u}/password` | page | **one-time reveal; revokes every session that account holds** |
| POST | `/admin/users/{u}/suspend` | 303 | revokes every session immediately |
| POST | `/admin/users/{u}/reinstate` | 303 | |
| POST | `/admin/users/{u}/sign-out` | 303 | ends their sessions without changing the password |
| GET | `/admin/projects` | page | every board and who can reach it |

**The two reveal routes return a page, not a redirect.** A redirect would have to carry the
password in a query string or a flash, and both persist it. The credential exists in that
one response and nowhere else — not in the database, not in a log line, not in history.

Refusals worth knowing: suspending your own account is **409**, and so is demoting the last
owner who can still sign in. Both would leave the installation with nobody able to
administer it.

## The move contract — **built**

The one endpoint with a hand-written client, so its shape is fixed here.

```
POST /cards/{c}/move
Content-Type: application/x-www-form-urlencoded
X-CSRF-Token: <session csrf>
X-Client-Id: <tab id>

to_lane=<lane uuid>
&to_order=<card uuid>&to_order=<card uuid>&…
&from_lane=<lane uuid>          # omitted on a same-lane reorder
&from_order=<card uuid>&…       # omitted on a same-lane reorder
```

The **source lane's order is sent too**, because removing a card shifts everything
below it, and the server writes dense positions rather than computing a shift.

Server side, in one transaction: read the board state, run
`internal/card/domain.Plan`, write every placement, write a `card_activity` row,
commit — then broadcast. A rejected move writes nothing.

An **in-lane reorder writes no activity row**: it is the most common gesture there is,
and logging it would bury a card's real history.

`to_order` and `from_order` are read with empties dropped. A form that submits
`from_order=` with no value arrives in Go as a slice holding one empty string, and
that would refuse the ordinary move of dragging the last card out of a lane.

The affected-lane list includes a lane the card **left** even when the plan wrote no
placement for it — an emptied lane produces none, and it is exactly the lane that
visibly changed.

| Outcome | Status | Body |
|---|---|---|
| Applied | 204 | empty — the result reaches other tabs over SSE |
| Target lane at its WIP limit | 409 | a toast fragment |
| Card or lane not on this board | 404 | a toast fragment |
| An order naming a card from another board | 404 | a toast fragment |
| `from_lane` disagrees with where the card actually is | 409 | a toast fragment |
| The same card twice, or the moved card missing from `to_order` | 400 | a toast fragment |
| No CSRF header | 403 | — |
| No grant on the card's board | 404 | a toast fragment — never 403 |

On any non-204 the client appends the toast and re-fetches both affected lanes via
`GET /lanes/{l}/fragment`, so the board snaps back to whatever the database
actually says rather than to a guessed undo.

## The live wire

| Event | Payload | Sent when |
|---|---|---|
| `lane-updated` | one or more `hx-swap-oob` lane fragments | a card is created, edited, moved, archived, deleted, commented on, or has a file attached |
| `board-dirty` | nothing | a lane or label changes, or the project is renamed |

**A card change is surgical; a structural change is a signal.** Not a performance
trade-off: an out-of-band swap cannot *relocate* an element, so a lane that was added,
reordered or deleted has no existing element to replace and no fragment can express it.
The client re-fetches `/projects/{p}/board` whole, which is rare and always correct.

`lane-updated` carries `ExceptClient` from the request's `X-Client-Id`, so the tab that
made the change never receives it — otherwise htmx would swap a card out from under the
hand that just dropped it. `board-dirty` deliberately does not: it comes from a full-page
form post rather than a fetch, and any *other* tab the same person has open still needs
it.

A lane the card **left** is included even when the plan wrote no placement for it. An
emptied lane produces none, and it is exactly the lane that visibly changed.

The board element carries **two** triggers for one re-fetch:

```html
hx-trigger="sse:board-dirty, board-dirty"
```

The first is the server's signal; the second is what `board.js` fires on
`htmx:sseOpen` after a reconnect. Events sent while a client was disconnected are gone
and nothing replays them, so a board that merely resumed listening would sit quietly
stale. The first open is skipped — the page was just rendered from the database.

## Middleware order

Global, on every request:

```
RealIP → RequestID → RequestLogger → Recoverer → SecurityHeaders → LimitBody
```

`RealIP` is first so the rate limiter and the logs see the actual client rather than
the proxy — which requires the reverse proxy to set `X-Forwarded-For`. If it does
not, every request shares one IP bucket, which fails closed rather than open.

Then, per group:

```
public          (nothing further)
  └─ POST /login    + LoginRateLimit

authenticated   + RequireSession → VerifyCSRF
  ├─ POST /logout
  └─ everything else  + RequirePasswordChange
```

`RequireSession` before `VerifyCSRF`, because the token it compares against lives on
the session. `RequirePasswordChange` last, so a user being held still has a session
and a CSRF token and can actually submit the form.

`LimitBody` runs globally, immediately after the security headers and **before anything
parses a body**. That ordering is load-bearing: the CSRF check has to read a multipart
form to find its token, and parsing one spools it to disk — so the ceiling has to exist
by then or an unbounded upload is written before anything can refuse it. It is set to
`UPLOAD_MAX_BYTES` plus a megabyte of envelope slack, and skips GET and HEAD so the SSE
stream is untouched.

**Not yet applied:** `Timeout` (exempting the SSE route) lands in phase 8.

