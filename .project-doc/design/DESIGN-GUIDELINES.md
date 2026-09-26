# Chonkboard — Design Guidelines

The system. One file, one source of truth. Every token here exists as a CSS
custom property in `web/css/input.css` under `@theme`; a hex code that appears in
markup but not in this table is a bug.

## What this is

A tool, not a landing page. It is looked at for hours, so it is quiet: one
accent, no gradients, no card shadows competing for attention, no illustration.
The board itself is the only thing with visual weight. Density beats generosity —
a lane should show six or seven cards without scrolling.

## Type

System font stack. Zero bytes, no FOUT, native rendering on every target. A
self-hosted webfont would buy a little personality at the cost of the first
paint, and this is a page people keep open, not one they land on.

| Token | Size | Line height | Used for |
|---|---|---|---|
| `text-2xs` | 0.6875rem | 1rem | card meta, counts, avatar initials |
| `text-xs` | 0.75rem | 1.125rem | lane names, buttons, labels, nav |
| `text-sm` | 0.8125rem | 1.25rem | card titles, form inputs, body |
| `text-base` | 0.9375rem | 1.4rem | card description prose |
| `text-lg` | 1.125rem | 1.6rem | modal titles |
| `text-xl` | 1.375rem | 1.8rem | page headings |
| `text-2xl` | 1.75rem | 2.1rem | login, empty states |

Ratio is ~1.2 (minor third) — deliberately shallow. A tool with a dramatic type
scale wastes vertical space that cards should be using.

## Colour

Warm-neutral greys rather than blue-greys, so the single warm accent belongs
rather than fights. Accent is burnt orange: uncommon in this category (every
board tool is blue), and it stays legible against both schemes.

| Token | Light | Dark | Role |
|---|---|---|---|
| `ground` | `#FBFAF9` | `#16161A` | page |
| `surface` | `#FFFFFF` | `#1F1F24` | card, modal, top bar |
| `surface-2` | `#F2F0EE` | `#26262C` | lane body |
| `surface-3` | `#E9E6E2` | `#2F2F36` | hover well, drop target |
| `line` | `#E3E0DC` | `#32323A` | decorative hairline |
| `line-strong` | `#949089` | `#74707B` | input border, control boundary |
| `ink` | `#1A1817` | `#EDEAE6` | primary text |
| `ink-2` | `#5C5854` | `#A8A39C` | secondary text |
| `ink-3` | `#8A857F` | `#7C7872` | meta, placeholder |
| `accent` | `#C2410C` | `#FB923C` | primary action, focus ring |
| `accent-hover` | `#9A3412` | `#FDBA74` | hover |
| `on-accent` | `#FFFFFF` | `#1A1817` | text on accent |
| `danger` | `#B42318` | `#F87171` | destructive, overdue, over-WIP |
| `warn` | `#A16207` | `#FBBF24` | due soon, high priority |
| `ok` | `#15803D` | `#4ADE80` | confirmation |
| `info` | `#1D4ED8` | `#7DA9FF` | informational |

Lane tints (`lane-slate|blue|teal|green|amber|rose|violet`) are the only palette a
manager chooses from. Seven options, all of comparable value, so no lane can be
made to shout over the others.

## Measured contrast

Computed with the WCAG relative-luminance formula, not estimated. Body text and
icons carrying meaning need ≥4.5:1; large text and control boundaries need ≥3:1.

### Light

| Pair | Ratio | Needs | |
|---|---|---|---|
| `ink` on `ground` | 16.97:1 | 4.5 | pass |
| `ink` on `surface` | 17.69:1 | 4.5 | pass |
| `ink-2` on `surface` | 7.05:1 | 4.5 | pass |
| `ink-2` on `surface-2` | 6.20:1 | 4.5 | pass |
| `ink-3` on `surface` | 3.66:1 | 3.0 | pass (meta only, never body) |
| `accent` on `surface` | 5.18:1 | 4.5 | pass |
| `on-accent` on `accent` | 5.18:1 | 4.5 | pass |
| `danger` on `surface` | 6.57:1 | 4.5 | pass |
| `warn` on `surface` | 4.92:1 | 4.5 | pass |
| `ok` on `surface` | 5.02:1 | 4.5 | pass |
| `info` on `surface` | 6.70:1 | 4.5 | pass |
| `line-strong` on `surface` | 3.18:1 | 3.0 | pass (input borders) |
| `line-strong` on `ground` | 3.05:1 | 3.0 | pass |
| focus ring (`accent`) on `ground` | 4.97:1 | 3.0 | pass |

### Dark

| Pair | Ratio | Needs | |
|---|---|---|---|
| `ink` on `ground` | 15.05:1 | 4.5 | pass |
| `ink` on `surface` | 13.68:1 | 4.5 | pass |
| `ink-2` on `surface` | 6.55:1 | 4.5 | pass |
| `ink-2` on `surface-2` | 6.01:1 | 4.5 | pass |
| `ink-3` on `surface` | 3.74:1 | 3.0 | pass (meta only) |
| `accent` on `surface` | 7.25:1 | 4.5 | pass |
| `on-accent` on `accent` | 7.82:1 | 4.5 | pass |
| `danger` on `surface` | 5.93:1 | 4.5 | pass |
| `warn` on `surface` | 9.83:1 | 4.5 | pass |
| `ok` on `surface` | 9.42:1 | 4.5 | pass |
| `info` on `surface` | 7.03:1 | 4.5 | pass |
| `line-strong` on `surface` | 3.40:1 | 3.0 | pass |
| focus ring (`accent`) on `ground` | 7.97:1 | 3.0 | pass |

`line` is excluded on purpose: it separates two surfaces decoratively and
identifies no control, so 1.4.11 does not apply to it. Anything that *is* a
control boundary uses `line-strong`, which is measured above.

Re-measure with `.project-doc/design/contrast.py` whenever a colour token
changes. A palette edit that skips this is how a build loses its accessibility
score three weeks later.

## Spacing, radius, shadow

Tailwind's 0.25rem base, unmodified. Lane width is a fixed `17rem` at every
breakpoint — a lane that reflows is a lane whose card count you cannot learn.

| Token | Value | Used for |
|---|---|---|
| `rounded-card` | 0.375rem | cards, buttons, inputs |
| `rounded-lane` | 0.5rem | lanes, modals |
| `rounded-pill` | full | counts, labels, avatars |
| `shadow-card` | 1px, 6% | a card at rest |
| `shadow-card-drag` | 8px/24px, 18% | a card being dragged — the only lift on the board |
| `shadow-pop` | 12px/32px, 22% | modal, toast |

## Motion

Two curves, three durations, and every one of them is opt-out in a single
`prefers-reduced-motion` block in `input.css`.

| Element | Trigger | Move | Duration | Curve | Reduced motion |
|---|---|---|---|---|---|
| Card | drag start | lift: `shadow-card-drag` + 1.5° rotate | 120ms | `--ease-out-quart` | shadow only, no rotate |
| Card | drag over | siblings slide | 150ms | `--ease-out-quart` | 0ms (Sortable `animation: 0`) |
| Lane | valid drop target | `outline` dashed accent + `surface-3` | 120ms | `--ease-in-out` | unchanged (colour only) |
| Card | live update arrives | none — the swap is instant | — | — | unchanged |
| Modal | open | opacity + 4px rise | 200ms | `--ease-out-quart` | opacity only |
| Toast | enter/leave | opacity | 200ms | `--ease-in-out` | opacity only |
| Button | hover | background colour | 120ms | `--ease-in-out` | unchanged |

A live update is deliberately **not** animated. A card sliding into place by
itself, while you are reading, reads as a glitch rather than as news.

There is no set-piece. This is a tool; the one engineered moment is that a drag
feels attached to the pointer, and that is spent on Sortable's tuning
(`delay: 120` on touch only, `touchStartThreshold: 4`) rather than on a flourish.

## Anti-slop rules

- One accent. Lane tints are not accents; they are labels.
- No card is a rounded box with a coloured icon in a circle.
- No gradient, anywhere.
- Asymmetry: the lane rail scrolls from the left edge, it is not centred with
  equal margins.
- Empty states say what to do, not "No items found".
- Every interactive element is a real `<button>` or `<a href>`. A `div` with a
  click handler is a bug, not a shortcut.

## Responsive

Specified at 390 / 768 / 1440. Base styles are the 390 design.

| | 390 | 768 | 1440 |
|---|---|---|---|
| Lane rail | horizontal scroll, one lane visible + peek | ~2.5 lanes | 5 lanes, no scroll |
| Lane width | 17rem | 17rem | 17rem |
| Top bar | logo mark only, name hidden | full | full |
| Board height | `100dvh - 3rem` | same | same |
| Card modal | full-screen sheet | centred, 32rem | centred, 36rem |
| Drag | touch, 120ms hold so a swipe still scrolls the rail | pointer | pointer |
| Admin tables | stacked rows | table | table |

`dvh` not `vh`: the mobile URL bar collapsing must not clip the last card in a
lane.
