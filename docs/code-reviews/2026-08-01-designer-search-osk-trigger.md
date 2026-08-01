# 2026-08-01 — Designer search box dead under the on-screen keyboard (ut-docs#196)

## What shipped

On the Designer page (`/designer`), typing a product name into the
search box showed no results — a regression. Root cause: PR #125's
on-screen keyboard (`web/public/osk.js`) types by calling `setRangeText`
then `dispatchEvent(new Event('input', {bubbles:true}))` — a tapped
virtual key never fires a native keydown/keyup. The search box's htmx
trigger was `hx-trigger="keyup changed delay:300ms"`, so OSK-driven
typing never fired the search request; a real keyboard worked fine,
masking the bug until a touch till hit it.

Fix, in `web/ui/partials/buttons_admin.html`:

- `hx-trigger="keyup ..."` → `hx-trigger="input changed delay:300ms"`.
  htmx's `input` trigger listens on the native `input` event, which
  fires for both real typing and the OSK's synthetic dispatch — a strict
  superset of `keyup` here (also catches paste and the `type=search`
  clear-button).
- `web/ui/pages/translations.html` carried the identical pattern
  (`keyup changed delay:300ms from:input[name='q']`) — same fix applied
  there (found by the independent reviewer, not the original triage).

## Independent review

Spawned via `Agent` (opus, different model from the implementing model),
briefed with the exact diff, the OSK/htmx mechanics, and told to
actually run build/vet/guards/tests, not just read the diff.

**Findings, triaged:**

1. **should-fix, fixed** — `web/ui/pages/translations.html:6` has the
   same `keyup`-only trigger, same OSK dead-end, unfixed by the original
   diff. One-word fix applied (`keyup` → `input`).
2. **should-fix, fixed** — the fix's second bug (below) was wrapped in
   `document.addEventListener('DOMContentLoaded', ...)`, which works but
   isn't this codebase's idiom and adds a latent trap: `buttons_admin`'s
   sibling `buttons_admin_grid` **is** returned via htmx swap
   (`internal/ui/buttons.go`), and if `buttons_admin` itself is ever
   swapped instead of full-page-rendered, `DOMContentLoaded` has long
   fired and the handlers would silently never register. `catalog.html`
   already establishes the correct idiom —
   `document.body.addEventListener('htmx:eventName', ...)` — since
   htmx's custom events bubble to `body` like any DOM event, needing no
   `htmx` global at all. Replaced both `htmx.on(...)` calls with plain
   `document.body.addEventListener(...)`, dropping the `DOMContentLoaded`
   wrapper entirely (the pre-existing `#search` `input` listener never
   needed it either, since `#search` is already parsed by the time its
   own script tag runs).
3. **nit, fixed** — the new Go regression test's
   `strings.LastIndex(body[:idx], "<input")` was unguarded; a `-1` (no
   match) would slice `body[-1:...]` and panic instead of failing
   cleanly. Added an explicit `if tagStart == -1 { t.Fatalf(...) }`.
4. **nit, fixed** — the new e2e spec copy-pasted `setOskMode` verbatim
   from `settings-osk.spec.ts`. Moved it into `e2e/tests/helpers.ts`
   (alongside the existing `watchConsole`) and updated both specs to
   import it.

No blocking findings. Repository-pattern/money/offline-first rules are
not implicated (no non-test Go code changed, no file I/O, no DB access).
No new user-facing strings. Demo data used in the new tests is the
existing seeded `Sparkling Water 500ml` (itm003, `001_init.sql`), not a
real client/shop name.

## A second, independently-discovered bug (same file)

While reproducing #196 in a real browser (no prior e2e spec had ever
visited `/designer`), `buttons_admin.html`'s inline `<script>` block
threw `ReferenceError: htmx is not defined` on every single page load.
Cause: `htmx.min.js` loads with `defer` in `web/ui/layouts/base.html`,
but per the HTML spec `defer` has no effect on inline scripts (only
`src` scripts) — so this inline script ran synchronously at parse time,
before the deferred `htmx.min.js` had executed. Functionally low-impact
(the `input`-based show/hide listener registered fine before the throw,
so search results still displayed) but a real, reproducible JS error on
every Designer page load, now fixed by the `document.body` idiom above
(no longer depends on load timing at all).

## Verified beyond automated tests

- **TDD claim re-verified independently by the reviewer**: with the
  template's `hx-trigger` reverted to `keyup` via `git stash`, the new
  Go test `TestDesigner_SearchBoxTriggerFiresOnSyntheticInputEvent`
  fails with the exact claimed message; restored, it passes.
- **Regression genuinely reproduced end-to-end, not just at the unit
  level**: with `hx-trigger` reverted, `designer-search.spec.ts`'s
  OSK-driven spec fails (no results appear) while the real-keyboard spec
  still passes — proving the bug is OSK-specific, matching the reported
  symptom exactly.
- **Second bug isolated**: with only the `document.body`
  idiom reverted back to the pre-review `DOMContentLoaded`-wrapped
  `htmx.on()` version, both e2e specs fail on `ReferenceError: htmx is
  not defined` (caught by the shared `watchConsole` helper).
- **No race from removing `DOMContentLoaded`**: `document.body`
  listeners for htmx custom events need no `htmx` global at registration
  time (only when the event actually fires, by which point htmx has
  always finished its own deferred init) — reviewer traced this
  explicitly rather than asserting it.
- **Full e2e suite run** (`e2e/tests`, default project, real Chromium):
  25 specs, 24 passed. The one failure
  (`catalog-image-to-till.spec.ts`, `toHaveJSProperty('complete', true)`
  timing out) was independently confirmed by both the implementer and
  the reviewer, via `git stash` to an unmodified tree, to fail
  identically on `main` — unrelated to this diff, a pre-existing
  environment issue in this sandbox.
- **Pre-existing Go failure re-confirmed**: `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`) fails identically on unmodified `main`
  (sandbox runs as root, bypassing a read-only-directory check) —
  already documented as pre-existing in universal-till#138's own test
  plan.
- `go build ./...`, `go vet ./...`, `scripts/ci/guard-data-access.sh`,
  `scripts/ci/guard-i18n.sh` all clean after every round of fixes.

## Safe to merge

Yes. No blocking findings; all should-fix/nit findings from the
independent review were applied and re-verified together (full Go
suite + full e2e default-project suite, both green apart from the two
confirmed-pre-existing failures above).

## Explicitly deferred

Nothing from this task. `internal/pages/buttons_api.go`'s `offset`
query param (pagination) has no reachable UI anywhere in Designer —
noted by the reviewer as a non-gap for this fix, not filed as new work
since it isn't part of #196's scope.
