# Tables floor plan: keyboard reposition (ut-docs#826)

**Reviewer**: independent Sonnet instances, fresh context, isolated
worktrees (a different instance from the one that wrote the code — this
card is `complexity:easy`, so per the scrum-master skill's model routing,
"different model" relaxes to "different instance"). Two rounds: the
second was earned by the first round finding a blocker.
**Branch**: `feat/826-tables-keyboard-reposition` (base: `main` @ `7b839a5`).
**Date**: 2026-08-22.

## What shipped

The floor-plan editor's SVG table tiles (`web/ui/partials/tables_state.html`,
`web/ui/pages/tables.html`) were repositionable by pointer-drag only — a
keyboard-only or switch-access operator could create/edit a table via the
plain HTML form but had no way to place it on the map.

- **`web/ui/partials/tables_state.html`** — every `.table-node` `<g>` now
  gets `tabindex="-1"` (default), `role="button"`, and an `aria-label`
  built from a new locale key.
- **`web/ui/pages/tables.html`**:
  - CSS: `.table-node:focus-visible { outline: 3px solid var(--focus-border); … }`
    — reuses the existing ut-docs#797 `--focus-border` token (curated to
    clear WCAG 1.4.11 in every theme), not raw `--accent`.
  - JS: `applyTabIndex()` toggles every `.table-node`'s `tabIndex` between
    `0`/`-1` in lockstep with `window.tablesEditing` (the same gate
    pointer-drag already reads at `pointerdown`) — called from the
    edit-toggle click AND from an `htmx:afterSwap` listener scoped to
    `#floorplan-wrap` (mirrors `catalog.html`'s existing
    `ev.detail.target.id === '...'` pattern).
  - JS: `persistPosition(node)` extracted as the one shared save path for
    both the existing pointer-drag `endDrag` and the new keyboard handler.
  - JS: an arrow-key nudge (`STEP=10`, `STEP_BIG=40` with Shift) through
    the same `clamp()` pointer-drag uses, debounced 400ms per node via a
    `Map<node, timer>` (`pendingSaves`), flushed on leaving edit mode.
- **`web/locales/{en,fa,tr,ar}.json`** — new `tables.node.aria` key, all
  four locales (guard-i18n requires exact key-set parity).
- **`web/help/{en,fa,tr,ar}/tables.md`** — a sentence describing the
  keyboard path, all four locales; `web/help/img/**` + `manifest.json`
  regenerated (`make docs-shots`).
- **`e2e/tests/tables-keyboard-reposition-826.spec.ts`** (new, 3 tests) +
  **`e2e/tests/helpers.ts`** (`ensureOperator`/`ADMIN_PIN`, mirroring
  `tests-docs/docs-shots.spec.ts`'s own login helper) +
  **`e2e/playwright.config.ts`** (`AUTH_ONLY_SPECS` now also matches this
  spec, routing it to the `auth` project).

### Ground truth the design rests on — independently re-verified

- **Server-side clamp is real and shared.** `internal/data/tables_repo.go`'s
  `clampToCanvas`/`SetTablePosition` bound every write regardless of
  client input; the client's own `clamp()` is correctly cosmetic (avoids a
  visible snap-back), not the source of truth — confirmed by reading the
  handler, not just the comment.
- **`GET /tables` has no `UT_AUTH=off` bypass** — `internal/pages/
  tables_page.go`'s `requireManager` 403s under the default e2e project
  exactly like `tests-docs/docs-shots.spec.ts`'s own `AUTH_TILL_TOPICS`
  list already documents (`"tables"` has been in that list since #814).
  Pre-existing, already-tracked gap, not something this card introduced or
  needed to fix — the new e2e spec is routed to the `auth` project instead
  of trying to route around it.
- **`%s` locale placeholders work the same way the existing `%d` ones do**
  — confirmed against `internal/httpx`'s `T`/`printf` wiring.

## Findings

### 1. BLOCKING (fixed here) — keyboard nudge silently dropped a move when focus moved to another table inside the debounce window

First round's independent review reproduced this live: `pendingSave` was a
single global `{node, timer}` slot. Nudging table A (scheduling its
debounced save), then Tabbing to table B and nudging it before A's
~400ms debounce fired, replaced the slot — A's move was never persisted,
with no error surfaced (the status line only reacts to a *failed*
request; here no request for A ever happened). `flushPendingSave` (called
on "Done editing") only ever flushed the *last* node too, so leaving edit
mode didn't rescue it either. This directly regressed the acceptance
criterion "same server-side clamp/persistence as pointer-drag" — pointer-
drag saves one node at a time on its own drag-end, with no shared state
between tables. The realistic failure mode is exactly the workflow a
keyboard operator arranging several tables would use: nudge one, Tab to
the next, nudge that one too.

**Fix**: `pendingSave` replaced with `pendingSaves`, a `Map<node, timer>` —
`scheduleKeyboardSave` and `flushPendingSave` both operate per-node, so
two tables' debounces never collide. A regression test was added (the
spec's third test) that nudges table A, Tabs to table B, nudges it before
A's debounce fires, and asserts both POSTs land and both positions
persist after reload. Second round tried harder to break the `Map`
approach (double-nudging the same node, a hypothetical third table) and
found no further issue with the mechanism itself.

### 2. Should-fix (fixed here) — non-English manuals not updated

`web/help/en/tables.md` gained the new sentence but `ar`/`fa`/`tr` were
left describing only pointer-drag. Per the standing product-owner
instruction (2026-08-06, ut-docs#324) the manual ships with the feature in
every shipped locale, not just English (unlike `web/locales/*.json`'s
external-plugin-pack split, help-doc translations aren't given a separate
advisory lane). `guard-help-topics.sh` stayed green throughout since it
only checks structure (topic existence, front matter, route coverage),
not translation currency — this is a prose-freshness gap only a human-
equivalent read catches, exactly as the reviewer skill's own "check the
manual shipped with the feature" step warns.

**Fix**: matching sentences added to `ar`/`fa`/`tr`, each reviewed by a
second Sonnet instance for structure/phrasing against the surrounding
existing content (not a native-speaker certification, but a genuine
translation attempt, not copy-pasted English or gibberish).

### 3. Should-fix (fixed here) — tabIndex not re-applied if editing starts before the initial SVG load resolves

The edit-toggle handler's original comment reasoning ("polling is paused
while editing, so no re-application is needed on swap") only covers the
*recurring* 15s poll, which is gated by `!window.tablesEditing`. The very
first `hx-trigger="load"` fetch is unconditional: if it resolves *after*
the operator has already clicked "Edit floor plan" (a slow query, a busy
till), the freshly-swapped nodes carry the template's default
`tabindex="-1"` and nothing ever re-applies `0`. Pointer-drag is
unaffected (it reads `window.tablesEditing` fresh at `pointerdown`), so
only the keyboard path silently degraded, with no visible symptom beyond
"Tab doesn't reach the tables."

**Fix**: `applyTabIndex()` extracted as a shared helper, called both from
the toggle click and from a new `htmx:afterSwap` listener scoped to
`#floorplan-wrap` (the same `ev.detail.target.id === '...'` pattern
`catalog.html` already uses). Confirmed it reads `window.tablesEditing`
fresh each call, not a captured value.

## Accepted, not fixed (out of scope / not a regression)

- **A narrow, self-healing race in the tabIndex-on-load fix itself**
  (found by round 2, deliberately trying to break the fix harder): if the
  very first `hx-get="load"` resolves *after* a keyboard nudge has already
  scheduled a debounced save on the pre-swap node, that node's timer still
  fires and correctly persists the move (detached-node `dataset` values
  survive, `persistPosition` doesn't throw or no-op) — but the newly-
  swapped visible node briefly shows the stale pre-nudge position until
  the next poll or a reload re-syncs it. Server state ends up correct;
  only the on-screen node lags briefly, and it self-corrects on its own.
  Needs three events to race within ~400ms on an already-slow till — not
  worth a fourth round of defensive plumbing for a card sized `easy`.
- **`alerts.png`/`designer.png`/`translations.png`/`sell.png` shifted a
  few hundred bytes each** across the two `make docs-shots` runs this
  card triggered, despite neither page/CSS being touched by this branch.
  Consistent with (a) the guard's surface hash being global, not
  per-topic — any `web/ui/**` change forces a full 84-shot regen — and
  (b) a real, already-known Chromium version mismatch in this sandbox
  (141.0.7390.37 reused vs. the `@playwright/test` pin's 149.0.7827.55;
  `resolve-chromium.sh`/`guard-docs-shots.sh` both treat this as a
  documented, non-fatal discrepancy, not a failure). `tables.png` itself
  is byte-identical in every locale in both regenerations — the actual
  feature change is correctly invisible in the default, non-focused view.
  Worth a separate look someday if `make docs-shots` non-determinism
  becomes its own problem; out of scope here.
- **Nested `role="button"`/`aria-label` inside the parent `<svg
  role="img">`** (`web/ui/partials/tables_state.html`): both reviewers
  judged this acceptable given the stated scope — keyboard *operability*
  for a sighted keyboard-only/switch-access user, not full screen-reader
  semantics (the #814 review that opened this card already treated the
  SVG's screen-reader opacity as separately mitigated by the Live-status
  text-fallback column in the HTML table listing).
- **Pre-existing `GET /tables` `requireManager` gap** (no `UT_AUTH=off`
  bypass) — already tracked via `tests-docs/docs-shots.spec.ts`'s
  `AUTH_TILL_TOPICS` list since #814; not this card's to fix.

## Verified beyond automated tests

- Both review rounds ran `gofmt -l .`, `go build ./...`,
  `go test ./internal/pages/... ./internal/data/...`, `guard-i18n.sh`,
  `guard-help-topics.sh`, and `guard-docs-shots.sh` — all green.
- The e2e spec (3 tests, including the blocker's regression test) was
  actually driven against a real browser and a real manager-authenticated
  till (`--project=auth`), not just read — both rounds ran it live and it
  passed both times.
- The keyboard nudge, debounce, per-node isolation, flush-on-exit, and
  focus-visible outline were all exercised for real, not just reasoned
  about — round 1 additionally wrote and ran a throwaway two-table race
  spec (since deleted) to first reproduce the blocker before it was
  fixed.

## Verdict

**Safe to merge.** No blockers or should-fix items remain after the
second round; both accepted items above are genuinely out of scope or
self-healing, not deferred defects.
