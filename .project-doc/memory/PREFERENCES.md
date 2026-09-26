# Preferences

Standing preferences for this repository. These are not architecture — they are how
the owner wants work done. They apply to any agent or person.

## Records

- Durable records live in **`.project-doc/`**. Never also `.claude-project/`;
  never a third location. Everything plain Markdown, harness-agnostic, no
  "as Claude I…" phrasing.
- `status/` and `memory/` are updated as work lands, not at the end. Stale status is
  worse than no status.
- Absolute dates, always. `2026-09-26`, never "yesterday".

## Code

- **Never add a comment that restates the code.** Comment the *why* — a constraint,
  a trade-off, a non-obvious rule. A comment that repeats the line above it is
  noise that rots.
- **Never create a README or documentation file unless asked.**
- Preserve existing naming and structure. If one slice does something a certain
  way, the next slice does it the same way.
- Write inside out: a rule belongs in `domain/` with a table test before any handler
  exists to call it.
- Reach feature parity before improving anything. When rebuilding or replacing a
  piece, never ship less than what was there.

## Git

- **Never commit unless asked.**
- **Never commit on `main` or `dev`.** Branch first — `feature/<name>` — even when
  the instruction is just "commit".
- No AI attribution in commits, pushes, or pull requests. No `Co-Authored-By` for a
  model, no "generated with" trailer.

## Verification

- Run the gate before calling anything done: `make check`.
- Verify claims, do not assert them. If a mechanism cannot be tested in the current
  environment, say so plainly and hand over the exact steps rather than implying it
  was checked.
- Prefer reading a library's source over guessing at its behaviour. It is usually
  faster and it is always more reliable.

## Communication

- Answer short first: a few plain sentences. The long analysis belongs in a file,
  not in the reply.
- Flag corrections to a plan explicitly, with the reason, rather than quietly
  changing course.
- Name what is *not* done and what is *not* verified. A completion report that
  omits a gap is a bug report waiting to happen.

## Reports

- A report goes to `~/Documents/<project>/` as both `.md` and `.pdf`, opens with
  the day's date, and never lives in the repository.
