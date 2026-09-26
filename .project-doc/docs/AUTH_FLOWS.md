# Auth and authorization

Cookie sessions, Argon2id passwords, a three-role model. No JWTs, no Redis, no
email.

## Why cookie sessions and not JWTs

`go-kit` in this workspace uses ES256 JWTs with a Redis blacklist. That is the
right shape for an API consumed by other services. It is the wrong shape here:

- A browser sends a cookie automatically; a server-rendered app has nowhere good
  to keep a bearer token. `localStorage` is reachable from any injected script.
- Revocation becomes a row update instead of a distributed blacklist. "Suspend
  this user and kill their sessions now" is one `UPDATE`.
- Dropping JWTs drops Redis, which is most of the reason this application has no
  second container.

The trade accepted: a session lookup is a database read per request. Against a
local SQLite file with a reader pool that is not a cost worth engineering away.

## Password storage

Argon2id, lifted verbatim from `go-kit/internal/shared/password/password.go`.
OWASP parameters: `time=3`, `memory=64MB`, `parallelism=4`, 32-byte key, 16-byte
salt. Encoded PHC string:

```
$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
```

Verification is constant-time (`subtle.ConstantTimeCompare`). The decoder rejects
a wrong version rather than guessing.

**Never use the real hasher in a test other than the password tests.** 64MB per
call makes a suite crawl; use a fake `Hasher`.

## Session lifecycle

```
POST /login
  ├─ rate limit: two token buckets — 30/min per IP, 5/min per email
  ├─ look up the user by lowercased email
  ├─ verify Argon2id  ── failure ──► generic "email or password is wrong"
  │    no account?  verify against a DECOY hash first, then the same error.
  │    Identical messages are not enough on their own: Argon2id is ~50ms, so
  │    returning early for an unknown address would be measurably faster and
  │    that timing difference is a working enumeration oracle.
  ├─ refuse if status = suspended
  ├─ mint: 32 random bytes → base64url  = the cookie value
  │        SHA-256 of it                = sessions.token_hash   (stored)
  │        32 more random bytes         = sessions.csrf_token
  ├─ Set-Cookie: chonk_session=<value>; HttpOnly; SameSite=Lax; Path=/
  │              Secure when APP_ENV != local
  └─ redirect: /account/password if must_change_password, else /
```

The raw token is **never stored**. A database leak yields hashes, not live
sessions.

Every subsequent request:

```
session middleware
  ├─ read the cookie          ── absent ──► redirect to /login
  ├─ SHA-256 it, look up by token_hash
  ├─ refuse if revoked_at IS NOT NULL or expires_at has passed
  ├─ load the user; refuse if suspended
  ├─ authctx.WithCurrentUser(ctx, user)
  └─ bump last_used_at, at most once a minute
```

The throttle on `last_used_at` matters: a board with SSE traffic would otherwise
write on every request, and SQLite has one writer.

Sessions are revoked by: logout, the user changing their own password (all other
sessions), an admin resetting the password (all sessions), and suspension (all
sessions). A ticker reaps rows past `expires_at`.

`SESSION_TTL` defaults to `168h`. Go durations have no day unit — seven days is
`168h`, not `7d`.

## CSRF

Cookie auth plus HTMX `POST`s is exactly the shape `SameSite=Lax` does not fully
cover: Lax still sends the cookie on a top-level cross-site navigation, so a
form-triggered `POST` from another origin is not automatically stopped in every
browser and configuration.

Each session carries a `csrf_token`. It reaches the page twice, both server-side:

```html
<body hx-headers='{"X-CSRF-Token":"…"}'>   <!-- every HTMX request carries it -->
<meta name="csrf-token" content="…">        <!-- board.js reads it for its fetch -->
```

`middleware.VerifyCSRF` compares it with `subtle.ConstantTimeCompare` on every
non-GET/HEAD/OPTIONS request and returns 403 on a mismatch. The header is checked
first because that is how every request from this application arrives; a
`csrf_token` form field is the fallback for a plain form with no JavaScript, which
is what the login and account pages use. No third-party dependency.

`ParseForm` is called there rather than only in the handler, and that is safe
because it caches: the handler still reads the body afterwards.

`board.js` sends it explicitly as `X-CSRF-Token` because a card move is a `fetch`
rather than an HTMX request — see `FRONTEND.md` for why.

## Roles

Two global roles, two project roles. A user's effective power on a board is the
better of the two.

| | Where it lives |
|---|---|
| `super_admin`, `member` | `users.role` |
| `manager`, `member` | `project_members.role` |

A super admin needs no `project_members` row; their access is global.

## Permission matrix

This table **is** the requirement. It resolves in exactly one place —
`internal/shared/authz`. Handlers call it; they never re-derive a rule inline.

```go
subject := authz.NewSubject(authctx.User(ctx), projectRole) // projectRole may be nil
subject.Can(authz.ActionLaneManage)            // ordinary actions
subject.CanOn(authz.ActionCardDelete, ownerID) // where "own only" applies
subject.CanManageBoard()                       // the named form of the requirement
```

`Can` is for actions that do not depend on who owns the thing. `CanOn` adds the
three where a member may act on their own work and nobody else's — deleting a card,
a comment, an attachment — and for every other action it is exactly `Can`, so a
handler can call `CanOn` unconditionally and stay correct.

Two properties worth knowing: the operator can do everything, stated once rather
than as a special case in twenty branches; and an **unknown action is refused**, so
a new action has to be added to the matrix and its test before it grants anything.

The table test covers all 25 actions against five standings — operator, project
manager, project member, signed in with no grant, anonymous — plus a suspended
account (which can do nothing, not even as a super admin) and an invalid role
string (which grants nothing). It is the most valuable test in the repository.

| Action | super admin | project manager | project member |
|---|:--:|:--:|:--:|
| Create a project | yes | — | — |
| Delete or archive a project | yes | — | — |
| Rename a project, edit its description | yes | yes | — |
| **Add, rename, recolour, reorder, delete a lane** | yes | yes | **no** |
| **Set or clear a WIP limit** | yes | yes | **no** |
| Create, edit, delete a project label | yes | yes | — |
| Grant or revoke project access | yes | members only | — |
| Promote someone to project manager | yes | — | — |
| Create a user account, set the initial password | yes | — | — |
| Reset anyone's password | yes | — | — |
| Suspend or reinstate a user | yes | — | — |
| See every project | yes | granted only | granted only |
| **Create a card** | yes | yes | **yes** |
| **Edit a card — every rich field** | yes | yes | **yes** |
| **Drag a card, within and across lanes** | yes | yes | **yes** |
| Delete a card | yes | any in project | own only |
| Comment on a card | yes | yes | yes |
| Delete a comment | yes | any in project | own only |
| Upload an attachment | yes | yes | yes |
| Delete an attachment | yes | any in project | own only |
| Attach or detach an existing label | yes | yes | yes |
| Change own password | yes | yes | yes |

A signed-in user with **no grant** on a board can do nothing to it at all — absence
of a `project_members` row is denial, and there is no negative grant.

The bold rows are the brief verbatim: members drag, create, and edit cards, and
nothing about the board's settings is reachable.

**Hiding a control is not authorization.** Every board-settings route sits behind
a manager-or-above check, so a hand-made `POST` with a member's cookie returns
403. The test plan verifies this with `curl`, because assuming it is how it stops
being true.

## Credential handover

There is no email in this system, by design. The flow is deliberate:

1. The owner creates the account in `/admin/users`.
2. A password is generated and **shown exactly once**, on that response. It is
   never emailed, never written to a log, never stored in plaintext.
3. `must_change_password` is set, so the user is redirected to
   `/account/password` on first login and cannot reach the board until they have
   chosen their own.
4. A reset does the same thing, and additionally revokes every session that user
   holds — otherwise a compromised session survives the reset that was meant to
   end it.

If the owner loses the one-time password before handing it over, they reset it.
That is the intended recovery path, and it is why there is no need for a
plaintext copy anywhere.

## What is deliberately absent

No email, no self-service signup, no "forgot password" link, no SSO, no 2FA, no
API tokens, no remember-me beyond the session TTL. Every one of them is additive
later, and every one of them would be a new attack surface on a tool with a
single operator and a handful of users.

## Threats considered

| Threat | Mitigation |
|---|---|
| Credential stuffing on `/login` | two rate limits (30/min per IP, 5/min per account); generic failure |
| Account enumeration | one message **and** one cost — the no-account path verifies a decoy hash so the timing matches |
| An attacker with one valid credential clearing their own throttle | a successful sign-in resets only the *account's* bucket, never the address's |
| Rate-limit state growing without bound | idle buckets swept every 10 minutes |
| Session theft from a database leak | only SHA-256 of the token is stored |
| Session fixation | a new token is minted on login; nothing is carried over |
| XSS reaching a session | `HttpOnly` cookie; templ escapes by default; markdown sanitised on render; CSP with no inline script |
| CSRF | per-session token compared in constant time on every non-GET |
| Privilege escalation via a hidden control | authorization at the route, verified by `curl` in the test plan |
| A member reaching another project's data | every mutation is scoped with `AND project_uuid = :project_uuid`, so a foreign id matches no rows and the row count refuses it |
| A leaked attachment URL | authorised per request against the card's project; returns 404, not 403, so it does not confirm the file exists |
| A stale session after a password change | every other session is revoked; the one that made the change is kept |
| A handed-over password staying in use | `must_change_password` holds every route until it is replaced |
| A corrupt stored hash read as a wrong password | a malformed hash is a 500, not a 401, so the bug surfaces instead of locking someone out |
| A suspended user continuing to work | sessions revoked on suspend; middleware refuses a suspended user on every request |
| Clickjacking | `X-Frame-Options: DENY`, `frame-ancestors 'none'` |
