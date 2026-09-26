# Brief

The original ask, and every decision taken while planning it. This is the source
material; `docs/` describes what was actually built from it.

## The ask, as given (2026-09-25)

> make a kanban board "Chonkboard":
> - it will have projects and then projects will have cards
> - the lane of the kanban board can be dynamically allocated (ex: backlog,
>   in-progress, testing, done, stash or whatever I want)
> - I will be super admin and give people access to certain projects (make
>   accounts for them, give them their credentials and change passwords when
>   needed)
> - they can not change any settings of the board. they can just drag the cards
> - they can make new cards
> - edit new cards
>
> stack: go, chi router, SQLite, goose migration, docker, docker compose if
> needed, Makefile, Tailwind CSS, HTMX, Alpine JS
>
> suggest me if you need any templating engine with HTMX like Templ

## Requirements, restated as testable statements

Each of these is something a test or a manual check can pass or fail. They are the
acceptance criteria for the product, and the permission matrix in
`../docs/AUTH_FLOWS.md` implements them.

| # | Requirement | Where it is enforced |
|---|---|---|
| R1 | A project contains lanes; a lane contains cards. | schema: `projects → lanes → cards` |
| R2 | Lanes are defined per project, named and ordered freely, created and removed at will. | `internal/board` |
| R3 | One super admin exists and is the only account that can create projects and user accounts. | `internal/shared/authz` |
| R4 | The super admin creates accounts directly, sets the initial password, and can reset any password later. | `internal/admin` |
| R5 | Access is granted per project: a member sees only the projects they were granted. | `project_members` + every project route |
| R6 | A member cannot change any board setting — not lanes, not the project, not membership. Refused at the route, not merely hidden. | `internal/shared/authz`, asserted by `curl` in the test plan |
| R7 | A member can create a card. | `internal/card` |
| R8 | A member can edit a card. | `internal/card` |
| R9 | A member can drag a card, within a lane and across lanes, on desktop and on touch. | `internal/card/domain.Plan` + SortableJS |

## Answer to the question that was asked

**Yes, use a templating engine, and `templ` is the right one.** HTMX's model is
that the server returns HTML fragments. With templ a fragment is a typed,
compiled Go function, so returning one card or one lane is compile-checked and
survives a refactor. `html/template` would make every fragment a stringly-named
runtime lookup and every typo a 500 rather than a build failure.

The cost is a `templ generate` step. `make build`, `make dev` (air) and the
Dockerfile all run it in the right order, so it is never a manual step.

## Decisions taken while planning, with the question that prompted each

These were put to the owner during planning and answered explicitly. The full
reasoning lives in `../memory/DECISIONS.md`.

| Question | Answer chosen |
|---|---|
| Templating engine | **templ** — typed, compiled components |
| How drag and drop works | **SortableJS + HTMX** — touch support out of the box, cross-lane, drop placeholder, auto-scroll |
| How much a card holds in v1 | **Rich** — labels with colours, due date, priority, assignee, comments, attachments |
| Who can create projects and lanes | **A per-project manager role in addition to the super admin** — a promoted member manages that one project's lanes and its member list, but cannot create projects or accounts |
| How much architecture to carry | **Pragmatic layered vertical slices** — not go-kit's full Hexagonal + CQRS with command and query buses |
| Live updates between viewers | **SSE**, in-process hub, per-project fan-out |
| Deployment | **Docker + compose + a volume** |
| Card attachments | **Yes** — files on disk beside the database, metadata rows |
| Backups | **Yes, automatic** — `VACUUM INTO` snapshots with rotation |

## Explicitly out of scope

Named so that they read as choices rather than as gaps. Any of them is a later,
additive change.

No email of any kind — passwords are handed over by the owner directly. No
self-service signup. No "forgot password" link. No multi-tenancy beyond project
grants. No card templates or recurring cards. No full-text search (SQLite FTS5 is
a small migration if it is ever wanted). No export or import. No audit log beyond
`card_activity`. No two-factor authentication. No public or anonymous board
sharing.

## Constraints that shaped the build

- **The stack was specified, not chosen.** Go, chi, SQLite, goose, Docker,
  Makefile, Tailwind, HTMX, Alpine. Everything else follows from making those
  work well together.
- **Single-operator deployment.** The owner runs this. That is why there is no
  Postgres, no Redis, no message broker, and no orchestration: one binary and one
  file that can be backed up by copying it.
- **This workspace had no precedent for half the stack.** No existing project
  here uses SQLite, templ, HTMX, Alpine, server-side Go templating, `go:embed`,
  or a Tailwind build outside npm. Chonkboard sets those patterns. What *was*
  inherited is listed in `../docs/PROJECT_KNOWLEDGE.md` under "What was borrowed".
