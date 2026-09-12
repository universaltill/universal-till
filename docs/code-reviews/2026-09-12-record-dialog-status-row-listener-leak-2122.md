# Code review: record-dialog.js status-row listener leak (ut-docs#2122)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2122
**Branch:** `fix/2122-record-dialog-status-row-listener-leak`
**Complexity:** easy (Dev: Sonnet inline, Review: fresh-context Sonnet subagent, isolated worktree)

## What shipped

`web/public/record-dialog.js`'s `bindStatusRow()` added a fresh pair of
`window` `online`/`offline` listeners for every `[data-record-dialog-conn]`
element it saw, guarded only by a `data-record-dialog-conn-bound` attribute
on that element. That guard is worthless against the real re-render path
this partial actually goes through: the `/items` rail
(`web/ui/partials/items_rail.html`, ut-docs#1950) `hx-get`s each section into
`#items-panel` in place, so navigating away from `/categories` and back
destroys the old status-row element and htmx fragments in a brand-new one —
which has never seen the bound-flag and gets its own fresh listener pair,
while the previous element's pair (and the detached element it closed over)
is never released. A till left open for a shift, with an operator tabbing
between `/items` rail sections many times, accumulated one leaked listener
pair (and one detached DOM node) per visit to a section carrying this
dialog, indefinitely.

Fixed by binding the `online`/`offline` listeners **once**, at module load,
in a new `paintStatusRows()` that repaints every currently-present
`[data-record-dialog-conn]` element from `navigator.onLine` on each event.
`bindStatusRow()` is kept only to force an immediate paint of a
freshly-swapped-in element (same call sites as before: `init()` and the
`htmx:afterSwap` handler).

## Independent review (fresh-context Sonnet subagent, isolated worktree)

Ran real code (build, vet, both e2e spec files) and independently
reproduced the TDD claim rather than trusting it.

**Verdict: PASS — merge-safe.**

### TDD re-verification (exact numbers)

Reverted `web/public/record-dialog.js` to `main`'s version only, re-ran the
new spec: **failed**, `Expected: 3, Received: 7` — baseline (3 `online`
listeners, from base.html's `#sb-conn` chip plus app.js's two unrelated
online/offline handlers) grew by exactly +1 per revisit across 4 extra
visits to `/categories`. Restored the fix, re-ran: passed again, working
tree byte-identical to before the revert.

### Findings

- **(Low, fixed by the reviewer)** The fix's own comment gave an inaccurate
  reason for why `bind()`'s pre-existing per-element bound-guard is
  harmless — it claimed `bind()`'s target "persists across a swap," but the
  reviewer verified that's false (the `[data-record-dialog]` element is
  swapped away and recreated exactly like the status row is, since both
  live in the same `#items-panel`-replaced fragment). The actual reason
  `bind()`'s guard is harmless: its `keydown` listener is attached directly
  to the dialog element itself, so it's garbage-collected with that
  detached node once nothing references it — a per-element guard there is
  only ever redundant, never leak-preventing, unlike this status row's
  listeners, which were attached to `window` (a target that outlives every
  swap). Comment rewritten to state the accurate mechanism; doc-only,
  re-verified `go build`/`go vet` clean and both spec files still 21/21
  green afterward.
- Checked and confirmed sound, no fix needed: `paintStatusRows()` is a
  stateless full repaint (no stale-state bug from dropping the per-element
  guard); no ordering/race risk (listeners register at module-parse time,
  before `DOMContentLoaded`, and the `data-conn-online`/`data-conn-offline`
  attributes are static server-rendered content already present the
  instant an element enters the DOM); the CDP session in the new e2e
  helper is wrapped in `try/finally` so it can't leak on a thrown error;
  the baseline-then-4-revisits delta approach is a sound proof for the
  diagnosed (unconditional, one-per-revisit) leak shape; no other file in
  the repo references the old `data-record-dialog-conn-bound` attribute;
  no markup/CSS touched, so no UI-visible change and no manual/help-topic
  update needed; the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll`, a cwd-relative path instead of `paths.Data`)
  don't apply (pure browser JS + a Playwright spec, no filesystem/Go code
  touched); no real client/shop name or secret-shaped literal anywhere in
  the diff.
- **(Cosmetic, not fixed)** Both call sites still pass `bindStatusRow(document)`,
  an argument the new no-arg signature ignores. Harmless (JS drops extra
  call args) and consistent with the neighboring `bind(document)` call on
  the same lines, which still needs its argument — not worth a diff churn.

## Verification

- `go build ./...`, `go vet ./...`: clean.
- `scripts/ci/guard-i18n.sh`: green (no new user-facing strings; JS status
  text was already routed through `data-conn-online`/`data-conn-offline`
  template attributes, unchanged here).
- e2e (real driven run against the actual production `/items` rail
  navigation path, not a synthetic event simulation): new
  `record-dialog-status-row-listener-leak-2122.spec.ts` (1 test, listener
  count via CDP `DOMDebugger.getEventListeners`) plus the existing
  `categories-record-dialog-2010.spec.ts` (20 tests) — **21/21 passed**.
- TDD claim independently re-verified by the reviewer via revert/restore in
  an isolated worktree (see above), not just taken on the author's word.

**Safe to merge.** No UI-visible change, no manual/help-topic update
required, no deferred follow-up items.
