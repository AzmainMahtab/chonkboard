# Chonkboard

A self-hosted kanban board. One static binary, one SQLite file, one `docker compose up`.

No Postgres, no Redis, no Node runtime at runtime, no SPA. You are the only
administrator: you create the projects, define each board's lanes, create the accounts
and hand out the credentials. The people you invite create, edit and drag cards — and
nothing about a board's settings is reachable to them.

## Running it

### Docker — the way it is meant to run

```bash
cp .env.example .env        # then edit SUPER_ADMIN_EMAIL
make up                     # build the image and start it
make logs                   # the first-boot password is printed here, once
```

Open <http://localhost:8080>. Everything lives in one named volume mounted at `/data`:
the database, uploads and backups.

```bash
make down                   # stop
```

### From source

You need **Go 1.27+**. Nothing else — `make tools` fetches the two code generators and
the Tailwind standalone binary, so there is no Node and no `package.json`.

```bash
make tools                  # templ + air into GOPATH/bin, Tailwind into ./bin
cp .env.example .env        # then edit SUPER_ADMIN_EMAIL
make run                    # generate, build, run
```

Open <http://localhost:8080>.

`make tools` is a one-off. After that, `make run` is enough — and `make dev` gives hot
reload on Go, templ and CSS changes.

> A fresh clone has no `*_templ.go` and no `app.css`: both are generated, and committing
> them would invite a merge conflict in a file nobody edits. `make build` and `make run`
> regenerate them, so a bare `go build` is **not** enough.

### First sign-in

On first boot with an empty database, Chonkboard creates the owner's account from
`SUPER_ADMIN_EMAIL`. If `SUPER_ADMIN_PASSWORD` is unset it generates one and prints it to
stdout **once** — it is stored only as an Argon2id hash, so there is no way to read it
back. You are asked to replace it immediately on signing in.

If you lose it before signing in, delete the database and start again.

## Using it

1. **Create a project.** It starts with five lanes — Backlog, In progress, Testing, Done,
   Stash — which you rename, recolour, reorder, give WIP limits, or delete.
2. **Create an account** in `/admin`. A password is shown **exactly once**; hand it over
   yourself, because Chonkboard sends no email. They must replace it before they reach a
   board.
3. **Grant them the project** from its settings page, as a member or as a manager.
4. They add cards, fill in the rich fields, comment, attach files, and drag things
   around. Every board open on that project updates live.

### Who can do what

| | Owner | Project manager | Project member |
|---|:--:|:--:|:--:|
| Create accounts, reset passwords, suspend | ✓ | — | — |
| Create, archive, delete a project | ✓ | — | — |
| Add, rename, reorder, delete a lane; set a WIP limit | ✓ | ✓ | **—** |
| Define the project's labels | ✓ | ✓ | — |
| Grant project access | ✓ | members only | — |
| **Create, edit and drag cards** | ✓ | ✓ | **✓** |
| Comment, attach a file, apply a label | ✓ | ✓ | ✓ |
| Delete a card, comment or attachment | ✓ | any on the board | own only |

Hiding a control is not authorization: every board-settings route refuses a member with
403 whether or not the interface offered it.

## Configuration

`.env`, read at boot. `.env.example` documents all of it; the ones you are likely to
touch:

| Variable | Default | |
|---|---|---|
| `APP_ADDR` | `:8080` | |
| `APP_ENV` | `local` | `production` requires `SESSION_COOKIE_SECURE=true` |
| `DB_PATH` | `./data/chonkboard.db` | |
| `SUPER_ADMIN_EMAIL` | — | required on first boot |
| `SUPER_ADMIN_PASSWORD` | — | leave unset to have one generated |
| `SESSION_TTL` | `168h` | Go durations have no day unit — seven days is `168h` |
| `UPLOAD_MAX_BYTES` | `10485760` | per file |

Behind a reverse proxy, set `SESSION_COOKIE_SECURE=true` and make sure the proxy sets
`X-Forwarded-For` — the sign-in rate limiter keys on the client address, and without it
every request shares one bucket.

## Working on it

```bash
make help              # every target, with a description
make check             # gofmt + vet + check-arch + go test -race — must pass
make check-postgres    # prove the migrations still run on PostgreSQL (needs docker)
make cover             # aggregate coverage
make migrate-up        # migrations also run automatically at boot
make seed-user EMAIL=person@example.com NAME="A Person"
```

The SQL is written to run on **PostgreSQL** unchanged, and `make check-postgres` proves it
against a real server rather than asserting it. Run it after touching `migrations/`.

### Where things are

```
cmd/chonkboard/      composition root, plus `migrate` and `seed` subcommands
migrations/          goose SQL, embedded with go:embed
internal/
  auth/              sessions, passwords, accounts
  project/           projects, membership, access resolution
  board/             lanes and labels
  card/              cards, comments, attachments, activity
  admin/             the owner's console
  platform/          config, database, httpserver, middleware, filestore
  shared/            authz, markdown, sse, ratelimit, apperrors, …
web/                 templ components, Tailwind source, vendored JS
.project-doc/        the project's records — read these first
```

Each slice is `domain → service → handler`, with `store.go` for its SQL. `domain/` imports
no framework, and `make check-arch` fails the build if that slips.

**`.project-doc/` is the real documentation.** Start with `docs/PROJECT_KNOWLEDGE.md` and
its traps, then `plan/ROADMAP.md` for what is built and what is not, and
`memory/DECISIONS.md` for why things are the way they are. `AGENTS.md` carries the rules
that apply when changing the code.

## Stack

Go · chi · sqlx · SQLite (`modernc.org/sqlite`, pure Go, `CGO_ENABLED=0`) · goose as a
library · templ · Tailwind v4 standalone CLI · HTMX · Alpine · SortableJS · goldmark +
bluemonday · Docker.

## Status

Phases 0–7 are done: the data layer, auth, projects and lanes, cards, the rich card, the
live board, and the admin console. Phase 8 — backups, a request timeout, the 404/500
pages, the responsive and accessibility pass — is not. See
`.project-doc/status/STATUS.md`.

One thing is worth knowing before you rely on it: **the drag-and-drop gesture has never
been exercised in a browser.** Everything behind it is proven over HTTP and against the
database, but no session so far had browser tooling available. If a drag misbehaves, that
is the first place to look.
