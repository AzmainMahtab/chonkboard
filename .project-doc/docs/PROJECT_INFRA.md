# Infrastructure

One binary, one file, one container. There is no second service to start, no
health ordering to get right, and no internal network to secure.

## What actually runs in production

```
docker compose up -d
  └─ chonkboard            alpine, non-root uid 1000, one static Go binary
       └─ /data            the only mutable state, on a named volume
            ├─ chonkboard.db  (+ -wal, -shm)
            ├─ uploads/       attachment bytes
            └─ backups/       VACUUM INTO snapshots
```

Migrations are embedded in the binary and run at boot. Assets are embedded too.
So a deploy is: build the image, replace the container, keep the volume.

## Build

`make build` → `make generate` → `templ generate`, then the Tailwind CLI, then
`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`. Roughly 8MB.

Order is load-bearing: the Go build fails without the generated templ files, and a
class added to a `.templ` has to reach the stylesheet. A bare `go build` skips
both and compiles against whatever was generated last.

### Dockerfile — three stages

| Stage | Base | Does |
|---|---|---|
| `css` | `alpine:3.21` | fetches the Tailwind standalone CLI, builds `app.css` from the `.templ` sources |
| `builder` | `golang:1.27-alpine` | `templ generate`, copies `app.css` in, `CGO_ENABLED=0 go build` |
| runtime | `alpine:3.21` | the binary, a non-root user, `/data`, a healthcheck |

The stylesheet gets its own stage because Tailwind scans the **markup**, not the
compiled Go — it needs `.templ` files, not the binary. Separating it also keeps
the CLI's ~120MB out of the final image.

`CGO_ENABLED=0` is why the driver must stay `modernc.org/sqlite`. Introducing
`mattn/go-sqlite3` breaks the static build and the runtime image with it.

There is no entrypoint script. Migrations run in-process, so there is nothing to
wait for and nothing to sequence.

## Configuration

Environment only, parsed once into a flat struct with `caarlos0/env/v11` and an
`envDefault` on every field. `.env` is read by Make and by docker compose; the Go
binary reads the environment. The two can never disagree about a value because
there is only one source.

| Variable | Default | Notes |
|---|---|---|
| `APP_ENV` | `local` | `local` or `production`. Drives log format and cookie `Secure` |
| `APP_ADDR` | `:8080` | listen address |
| `APP_PORT` | `8080` | host port compose publishes |
| `APP_BASE_URL` | `http://localhost:8080` | for absolute links |
| `LOG_LEVEL` | `info` | `debug` `info` `warn` `error` |
| `DB_PATH` | `./data/chonkboard.db` | `/data/chonkboard.db` in the image |
| `DB_MAX_READERS` | `0` | read-pool size; 0 means one per CPU. The writer pool is always 1 |
| `DB_CONN_MAX_LIFETIME` | `0s` | recycle pooled connections; 0 means never |
| `SESSION_TTL` | `168h` | **Go durations have no day unit** — seven days is `168h` |
| `SESSION_COOKIE_SECURE` | `false` | must be `true` behind HTTPS |
| `SUPER_ADMIN_EMAIL` | — | first boot only |
| `SUPER_ADMIN_PASSWORD` | — | leave unset and one is generated and printed once |
| `UPLOAD_DIR` | `./data/uploads` | |
| `UPLOAD_MAX_BYTES` | `10485760` | 10 MiB per file |
| `BACKUP_DIR` | `./data/backups` | |
| `BACKUP_INTERVAL` | `6h` | |
| `BACKUP_KEEP` | `28` | oldest pruned past this |

`.env.example` is the committed template. `.env` is gitignored.

## Makefile

`make help` lists everything. The groups that matter:

| | |
|---|---|
| **Toolchain** | `tools` — installs templ and air into `GOPATH/bin` and the Tailwind CLI into `bin/` |
| **Codegen** | `templ` `templ-watch` `css` `css-watch` `generate` |
| **Build** | `build` `run` `dev` (air: hot reload on Go, templ, and CSS) |
| **Migrations** | `migrate-up` `migrate-down` `migrate-reset` `migrate-status` `migrate-create NAME=…` |
| **Accounts** | `seed` (the operator) · `seed-user EMAIL=a@b.co NAME="A Name"` |
| **Data** | `seed` `backup` |
| **Gates** | `test` `cover` `cover-html` `fmt` `fmt-check` `vet` `lint` `check-arch` `check` `check-postgres` |
| **Docker** | `up` `watch` `down` `logs` |
| | `clean` — removes the binary, the stylesheet, and every `*_templ.go` |

### Migrations run through the binary, not a CLI

`make migrate-up` is `go run ./cmd/chonkboard migrate up`. Goose is used as a
**library** with `go:embed`, not as an installed command.

The reason: installing the goose CLI with SQLite support means either CGO or
fighting build tags. `go-chi-hex/Dockerfile` in this workspace installs it with
`-tags 'postgres'` precisely to *exclude* the SQLite driver. Using the library
gives one code path shared by boot and the command line, and keeps the runtime
image at one binary.

### `cover` and the per-package numbers

`make check` prints per-package coverage. Those figures understate the handler
slices, because Go credits cross-package coverage to the package the *test* lives in
and the project and board handlers are driven through the assembled router from
`httpserver`'s tests.

`make cover` reports the honest aggregate, using `-coverpkg`. It deliberately omits
`-race`: combining `-race` with `-coverpkg` over `./...` writes a profile whose
counters are all zero, which reads like a broken build. The race detector runs in
`test` and `check`.

### `seed-user`

```bash
make seed-user EMAIL=person@example.com NAME="A Person"
```

Creates an account and prints its password **once**. The same contract the admin
console will have in phase 7: the password is stored only as an Argon2id hash, there
is no plaintext copy anywhere, and `must_change_password` is set so the person has to
choose their own before they can reach a board. Losing it means resetting it.

Grant them a project from that project's settings page.

### `check-postgres`

Starts a throwaway `postgres:17-alpine` container, applies the **real** embedded
migrations to it with goose's PostgreSQL dialect, round-trips the timestamp and
boolean types, asserts the constraints and the partial index exist, exercises the
portable upsert, rolls everything back, and removes the container.

It is a build-tagged test (`-tags postgres`), so `make check` needs no server. Run
it after touching anything in `migrations/`. If it fails, a migration has drifted
into dialect-specific DDL and the move to PostgreSQL is no longer a configuration
change.

Needs docker. The image, container name and port are Makefile variables
(`PG_CHECK_IMAGE`, `PG_CHECK_NAME`, `PG_CHECK_PORT`) so it can be pointed at an
existing server instead.

### `check-arch`

Greps the import list of every `*/domain` package and fails on chi, sqlx, the
SQLite driver, templ, goose, `net/http`, or the env parser. Borrowed from
`go-kit/Makefile`.

It is not a style rule. It is what keeps every business rule testable without a
server, a database, or a template — which is why
`internal/card/domain/move_test.go` runs in milliseconds and covers fifteen cases.

## Backups

A ticker runs `VACUUM INTO /data/backups/chonkboard-<timestamp>.db` every
`BACKUP_INTERVAL`, keeping the newest `BACKUP_KEEP`. `make backup` does one now.

**`VACUUM INTO` is the correct way to snapshot a live SQLite database.** Copying
the file while WAL is active gives a torn result: the `.db` and its `-wal` are not
consistent with each other at an arbitrary instant. `VACUUM INTO` produces a
compacted, transactionally consistent file.

To restore: stop the container, replace `chonkboard.db` with a snapshot, delete
any `-wal` and `-shm` beside it, start.

Attachments are files, not rows — a full backup is a snapshot **plus**
`uploads/`. The volume covers both; a `.db` snapshot alone does not.

## Observability

Structured `slog` — text when `APP_ENV=local`, JSON otherwise. One line per
request with method, path, status, bytes, duration, and the request id. `/static/*`
is skipped so real requests are not buried. An SSE stream is logged once, when it
ends, rather than left unlogged for hours.

`/healthz` is liveness and the Docker healthcheck.

No Prometheus, no tracing. A single-operator tool with a handful of users does not
earn the operational surface; `docker compose logs -f app` is the debugging tool.

## Security posture at the edge

Headers on every response, including errors, from
`internal/platform/middleware.SecurityHeaders`: CSP, `nosniff`,
`Referrer-Policy: same-origin`, `X-Frame-Options: DENY`,
`Cross-Origin-Opener-Policy: same-origin`, `Permissions-Policy` denying camera,
microphone, and geolocation.

**Put it behind TLS.** Sessions are cookies; over plain HTTP they are readable in
transit. Set `SESSION_COOKIE_SECURE=true` and `APP_ENV=production` behind a
reverse proxy that terminates TLS, and make sure that proxy sets
`X-Forwarded-For` — `RealIP` runs first so the login rate limiter sees the actual
client rather than the proxy.

## Graceful shutdown

SIGINT or SIGTERM cancels one context that the HTTP server and every long-lived
goroutine share. In-flight requests drain; open SSE streams end when their request
contexts are cancelled. Fifteen-second grace, then a hard close.

The HTTP server has **no `WriteTimeout`** and `/projects/{p}/events` is exempt
from the request timeout. Either one would sever a live board mid-session. Idle
connections are culled by the SSE heartbeat instead.

## Scaling, honestly

This design runs on one machine and does not scale horizontally. SQLite is a local
file and the SSE hub is in process, so two replicas would have two databases and
two sets of subscribers.

That is the right trade for the brief — one operator, a handful of users, a board
per team. If it ever needs to scale out, the order is: swap the store layer for
Postgres (the port interfaces already isolate it), then move the hub behind
Redis pub/sub or NATS. The domain and service layers do not change, which is what
the layering is for.
