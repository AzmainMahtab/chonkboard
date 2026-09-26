# Front end

Server-rendered HTML with four small libraries. No SPA, no bundler, no Node.

## Budget

Vendored, pinned, committed. No CDN — so the CSP stays tight and the app works on
a disconnected network.

| Library | Version | Gzipped | Why it is here |
|---|---|---|---|
| htmx | 2.0.8 | 16.6KB | fragment swaps: the whole interaction model |
| htmx-ext-sse | 2.2.4 | 2.6KB | the live board |
| Alpine | 3.15.1 | 16.2KB | modal state, dropdowns, toast dismissal — the small local state templ should not own |
| SortableJS | 1.15.6 | 14.9KB | drag and drop, including touch |
| `board.js` | — | ~1.5KB | our own wiring |

**~52KB gzipped total.** Adding a fifth library needs a reason in the PR
description. The stylesheet is 5.5KB gzipped.

## Layers

```
web/view/          plain structs — the UI's only data shape
web/layouts/       Base (the only <html>), App (nav, CSRF, modal + toast hosts)
web/pages/         one per route that renders a document
web/components/    one per fragment a handler can return
web/static/js/     board.js
web/static/vendor/ the four libraries
web/css/input.css  @theme tokens — the single source of design truth
```

`web/` is a leaf of the dependency graph. A component takes a `web/view` struct,
never a domain type; a handler maps domain → view. That is what stops a template
change from touching a business rule.

`web/assets.go` embeds `static/` with `go:embed`, serves it `immutable` with a
content-hashed `?v=` suffix derived from a hash of every asset, and refuses
directory listings.

## templ

Chosen over `html/template` because HTMX returns fragments, and a fragment should
be a typed function rather than a stringly-named runtime lookup.

- A handler returns **one** component: a page, or exactly one fragment.
- `templ generate` must run before `go build`. `make build`, `make dev`, and the
  Dockerfile all do it in order; a bare `go build` does not.
- `*_templ.go` is generated and gitignored. Never edit it.
- templ injects its own `github.com/a-h/templ` import into generated code — do
  **not** import it explicitly in a `.templ` file or the build breaks with
  "templ redeclared in this block". `templ.Attributes` is available without it.
- Conditional attributes go through an `templ.Attributes` spread (`{ attrs... }`),
  which is how `Lane` and `LaneOOB` share one implementation.

## Tailwind v4, from the standalone CLI

No `package.json`, no Node, no bundler plugin. One downloaded binary in `bin/`,
invoked from the Makefile and from a dedicated Docker stage.

- Tokens are CSS custom properties in `@theme` in `web/css/input.css`. That file
  is the single source of design truth; `../design/DESIGN-GUIDELINES.md` documents
  it and carries the measured contrast table.
- **No `tailwind.config.js`.** This is v4; configuration is CSS.
- `@apply` appears only inside `@utility` blocks. In v4, `@apply` accepts real
  utilities only — a bare class in `@layer components` cannot be applied, which is
  why `btn`, `field`, and friends are declared with `@utility`.
- **A class name assembled at runtime is invisible to the scanner.** Helpers that
  pick a class return the whole literal string, which is why
  `web/components/helpers.go` is a switch over complete class names rather than a
  prefix plus a variable. Getting this wrong produces an element that is silently
  unstyled and nothing that looks like an error.
- `@source` globs are explicit in `input.css` and include the `.go` helpers, not
  only `.templ`.

## Drag and drop

SortableJS, not native HTML5 drag events: those do not fire on touch devices, so
native DnD would have made the board desktop-only.

```js
new Sortable(laneCardsEl, {
  group: "board",            // one group name across every lane = cross-lane drag
  animation: REDUCED ? 0 : 150,
  ghostClass: "card-ghost", chosenClass: "card-chosen", dragClass: "card-drag",
  draggable: "[data-card-uuid]",
  delay: 120, delayOnTouchOnly: true,   // a swipe still scrolls the lane rail
  touchStartThreshold: 4,
})
```

`delayOnTouchOnly` with a 120ms hold is the difference between a usable phone
board and one where every attempt to scroll picks up a card.

### The DOM contract

`board.js` reads these and nothing else. Changing an attribute name in a template
without changing `board.js` breaks the board silently, so they are listed here.

| Attribute / id | On | Used for |
|---|---|---|
| `data-lane-cards` | the card container | what Sortable binds to |
| `data-lane-uuid` | lane and card container | identifies the lane in a move |
| `data-card-uuid` | each card | the order arrays |
| `data-wip-limit` | the card container | client-side pre-check before the round trip |
| `data-project-uuid` | `#board` | building the SSE URL |
| `#live` | the SSE host | `board.js` rewrites its `sse-connect` |
| `#toasts` | the toast host | `beforeend` swap target |
| `#modal` | the modal host | every `hx-target` for a dialog |
| `meta[name=csrf-token]` | `<head>` | the header on `board.js`'s fetch |

### Why a move is `fetch` and not `htmx.ajax`

The order arrays are repeated form keys (`to_order=a&to_order=b`), and the request
carries two custom headers. A hand-rolled `fetch` with `URLSearchParams` is
explicit about both, and a 204 needs no swap. Errors come back as HTML and are fed
to htmx with `htmx.swap("#toasts", html, {swapStyle:"beforeend"})`, so the toast
still renders through the same component.

### Failure handling is server-authoritative

A rejected move does not attempt a client-side undo. `board.js` re-fetches both
affected lanes from `GET /lanes/{l}/fragment`, so the board shows whatever the
database says. Guessing an inverse of a drag is how a board and its data drift
apart.

## Live updates

### Whole lanes, not single cards

This is the correction that matters most, and it is not obvious.

An out-of-band `outerHTML` swap replaces an element **where it already sits**. It
cannot relocate a card from one lane's container into another's. Doing a cross-lane
move surgically would mean positional OOB targets
(`hx-swap-oob="beforebegin:#card-<next>"`) and index arithmetic on the client,
against a DOM that may have changed.

So **every card mutation broadcasts the affected lanes**, each as
`hx-swap-oob="outerHTML:#lane-<uuid>"`. Correct by construction, and a lane is a
few hundred bytes.

Structural changes — a lane added, renamed, reordered, deleted — broadcast a
`board-dirty` signal instead, and `#board` re-fetches itself whole. A structural
change is rare, so surgery is not worth it.

### The wire

```
event: lane-updated
data: <section id="lane-doing" … hx-swap-oob="outerHTML:#lane-doing">…</section>
data: <section id="lane-backlog" … hx-swap-oob="outerHTML:#lane-backlog">…</section>

: ping                     ← every 25s, under the usual 30s/60s proxy idle timeout
```

Every line of the payload carries its own `data:` prefix — an HTML fragment is
multi-line and a bare newline would end the event early, delivering truncated
markup. `internal/shared/sse.encode` handles this and has a test for it.

`hx-swap="none"` on `#live` is correct: htmx's out-of-band pass runs **before**
the main swap, so `none` applies the OOB content and touches nothing else.
Verified by reading the vendored htmx source, not assumed.

### The drag guard

**`htmx:beforeSwap` never fires for an SSE message.** The extension calls htmx's
`swap()` directly rather than going through the request pipeline. The cancelable
event it does fire is `htmx:sseBeforeMessage`.

So: while a drag is in flight, `board.js` cancels that event and queues the raw
payload; on drop it replays each one with
`htmx.swap(live, data, {swapStyle:"none"})`. Without this, a teammate's update
would pull a card out from under the pointer mid-drag.

`htmx:oobAfterSwap` re-binds Sortable, because an OOB swap brings in a new card
container that no Sortable instance is attached to.

### Echo suppression is per tab, not per session

`board.js` generates a `crypto.randomUUID()` per tab, appends it to the SSE URL as
`?client=`, and sends it as `X-Client-Id` on every mutation. The hub skips exactly
that subscriber.

Suppressing by session id would break a user with two tabs open: they share a
session, so the second tab would go stale.

## Alpine

Used only for state that is genuinely local and genuinely ephemeral: whether a
modal is open, a dropdown, a toast's own dismissal timer. Anything the server
knows belongs to the server.

Alpine compiles inline expressions with `new Function`, which is why the CSP
carries `script-src 'self' 'unsafe-eval'`. Alpine's CSP build removes that at the
cost of banning inline expressions in markup — a reasonable trade later, but it
changes every component's authoring style.

## Accessibility

Not a polish pass. These are build requirements.

- **Every drag has a keyboard equivalent.** A "Move to lane…" action in the card
  menu hits the same `POST /cards/{c}/move`; lanes reorder from the settings form.
  A board that can only be dragged is unusable without a pointer.
- Cards are focusable and open on `Enter`. Lanes are `role="list"`, cards
  `role="listitem"`, each lane labelled by its heading.
- One focus treatment for the whole app, defined once on `:focus-visible`, measured
  at 4.97:1 against the page in light and 7.97:1 in dark.
- Modals trap focus and restore it to the card on close.
- A failed drag is announced: the card silently snapping back looks like nothing
  happened, so the toast is `role="alert"`.
- Every interactive element is a real `<button>` or `<a href>`. A `div` with a
  click handler is a bug.
- One `prefers-reduced-motion` block in `input.css` covers the app, and Sortable's
  animation is set to 0 under it.
- A live update is deliberately **not** animated. A card sliding into place by
  itself while you read looks like a glitch, not like news.

## Content Security Policy

```
default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self';
img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'self';
frame-ancestors 'none'; base-uri 'none'; object-src 'none'
```

Everything is same-origin — no CDN, no external font, no inline script. The one
concession is `unsafe-eval` for Alpine, explained above.
