# Chonkboard — Project Records

Durable, agent-readable records for this repository. Everything here is plain
Markdown (or plain Python/YAML/JSON for data and scripts) so that **any** AI agent
or harness can read and write it — Claude Code, opencode, Cursor, Aider, Copilot,
Codex, or a person with an editor. Nothing here depends on a particular tool, and
nothing here is phrased as "as Claude I…".

## Precedence

This is the **only** records directory for this repository. If a
`.claude-project/` directory ever appears at the repository root, one of the two
is a mistake — consolidate into this one and delete the other. Never maintain
both.

## Contract

These files are **both input and output**.

**Before starting work**, read in this order:

1. `docs/PROJECT_KNOWLEDGE.md` — architecture, conventions, and the traps that
   have already cost someone time. Read the traps even if your task looks
   unrelated to them.
2. `status/STATUS.md` — what is actually built right now, phase by phase.
3. `plan/ROADMAP.md` — what comes next and why it is ordered that way.
4. `memory/DECISIONS.md` — so you do not re-litigate a settled choice.
5. Whichever of `docs/` is relevant to the task.

Also read `../AGENTS.md` at the repository root: it holds the binding
architecture rules and wins over any code comment.

**After finishing work**, update:

- `status/STATUS.md` — the single source of truth for "is this done?". Stale
  status here is worse than no status.
- `memory/DECISIONS.md` — any choice a future reader would otherwise re-argue.
  Say *why*, not just what.
- `memory/LEARNINGS.md` — anything surprising you had to find out the hard way,
  especially something that cost you a debugging session.
- `docs/` — when the architecture, route surface, or schema actually changed.
- `plan/ROADMAP.md` — when a phase's scope genuinely moved, not when you merely
  finished one.

Do not record chat transcripts, task-local notes, or anything already visible in
the code or in `git log`. Record what the code cannot tell you: *why it is this
way*, *what is not done yet*, and *what will bite the next person*.

## Layout

| Path | Holds |
|---|---|
| `context/` | The source material the project was specified from — the original brief and the decisions taken while planning. |
| `docs/` | How the system works today: architecture, routes, database, infrastructure, auth, front end. |
| `memory/` | Decisions, learnings, and standing preferences. Append-only in spirit. |
| `plan/` | The phased roadmap and its rationale. Every phase, its deliverables, and its gate. |
| `status/` | Current implementation state per phase, and what is verified versus merely written. |
| `design/` | The design system: tokens, the measured contrast table, responsive spec, and the script that re-measures contrast. |
| `agents/` | How to work in this repository, whichever agent or person you are. |

## Repository map

```
chonkboard/
├── AGENTS.md              binding architecture rules — read before writing code
├── Makefile               every task; `make help` lists them
├── Dockerfile             3 stages: stylesheet → binary → runtime
├── docker-compose.yml     one service, one volume. That is the whole stack.
├── cmd/chonkboard/        composition root + migrate/seed/backup subcommands
├── migrations/            goose SQL, embedded in the binary
├── internal/              the application, sliced by feature
├── web/                   templ templates, tokens, vendored JS, embedded assets
└── .project-doc/          you are here
```

## Conventions for writing in here

- One idea per heading. A reader arriving mid-task should be able to skim.
- Absolute dates (`2026-09-26`), never "last week" or "recently".
- Name files and symbols with their real paths, so a grep finds them.
- When a document and the code disagree, the code is right and the document is a
  bug — fix the document in the same change.
