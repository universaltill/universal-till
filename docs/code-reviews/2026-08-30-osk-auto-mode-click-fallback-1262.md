# Code review: osk.js auto mode also enables from a click (ut-docs#1262)

**Branch:** `fix/1262-osk-auto-mode-pointerdown-fallback`
**Repo:** `universal-till`
**Reviewer(s):** fresh-context Sonnet subagent (draft 1, pointerdown-based),
then Opus (draft 2, click-based) — `complexity:medium` routing per the
`scrum-master` skill's model table.

## What shipped

`web/public/osk.js`'s `auto` mode enabled native-keyboard suppression
either immediately (capability queries report touch) or lazily on the
page's first `touchstart`. A device whose touch is misreported as mouse
input (ut-docs#1238's real case — an Android tablet's Chrome in "desktop
site" mode) never fires `touchstart` at all, so `enabled` never flipped
and suppression never ran anywhere on the page.

Fix: the first `click` of any kind also enables (`guardSweep(document)` +
`updateToggles()`), closing the gap at the source rather than per-dialog.
Accepted, documented trade-off: this also enables for a genuine
mouse-only desktop session on its first click, since there is no reliable
signal (pointerType included) that distinguishes a misreporting device's
synthetic events from a real mouse's.

Two new tests in `e2e/tests/osk-central-guard.spec.ts`:
- the fallback fires on a plain click, in a non-touch browser (a real
  stand-in for the misdetected-device case), and a field added afterward
  is swept too.
- a realistic-duration press (`{ delay: 120 }`, not an instant synthetic
  `.click()`) on the sale screen's scan-row submit — the FIRST interaction
  on a fresh page — still reaches the basket.

Regenerated `web/help/img/**` + `manifest.json` (`make docs-shots`),
required by `guard-docs-shots.sh` for any `web/public/**` change.

## What the independent review found

**Draft 1** (fresh-context Sonnet review) bound the fallback to
`pointerdown` *and* `click`, deferring the actual enable one tick via
`setTimeout(fn, 0)` — reasoning (confirmed necessary by that same review)
that running `guardSweep()`/`updateToggles()` synchronously from
`pointerdown` reveals the scan-row's OSK toggle mid-click, shifting the
Add button out from under a still-in-flight click and silently dropping
it. Reproduced directly against `make docs-shots`'s own `sell` screenshot
tests before the deferral existed.

**Draft 2** (Opus review) found that the `setTimeout(0)` deferral is only
long enough to survive a *synthetic, instant* `.click()` — not a real,
human-scale press. Instrumented probe:

```
HELD PRESS ORDER (120ms):    ["pointerdown","timeout-from-pointerdown","mouseup","click"]
FAST CLICK ORDER (.click()): ["pointerdown","mouseup","click","timeout-from-pointerdown"]
```

On a real press (or the review's own `{ delay: 120 }` e2e probe), the
timeout fires *before* mouseup/click, not after — the scan-row toggle
reveal still lands mid-press and drops the tap, on exactly the misdetected
hardware this ticket is about, which holds contact for real duration.

**Fix (blocking, applied before merge):** move the trigger to `click`
alone, drop `pointerdown` entirely. `click` fires only once the whole
interaction's own hit-testing is already resolved, so nothing the handler
does afterward can misdirect it — no deferral needed for correctness
(kept anyway, for consistency with `touchstart`'s de-dup guard and to stay
out of the way of other click-delegated handlers in the same dispatch).
Verified: the WebKitGTK `pointerdown`-preventDefault hazard (ut-docs#1219)
doesn't apply — the only `pointerdown.preventDefault()` call sites in the
codebase (`osk.js`'s own keys/toggle, `tables.html`'s designer drag,
`bugreport_panel.html`'s drag header) are never the first interaction on a
page.

The shipped test suite was upgraded accordingly: the realistic-press test
now uses `.click({ delay: 120 })`, not a plain `.click()` — the review
noted a plain click would have passed even against the buggy
`pointerdown`+`setTimeout` draft, which is exactly why that draft's first
version of the test didn't catch the bug. Independently re-verified: the
upgraded test fails (times out waiting for `/api/pos/...`) against the
`pointerdown`-based draft, passes against the shipped `click`-based fix.

## Non-blocking, accepted

- **One-interaction native-keyboard gap on a misdetected device's very
  first tap.** `enabled` only flips inside the deferred timeout, so the
  first field tapped is *focused* (and the native IME's fate decided at
  focus time, per this file's own ut-docs#1022/#155 handling) before
  `guardSweep()` has run. Every tap after the first, on any field, is
  correctly guarded. Strictly better than the reported status quo (native
  keyboard forever) and the same shape of gap the `touchstart` path
  already carries on a genuinely fresh page's very first touch — not a new
  class of bug, just a narrower version of an accepted one. Documented
  inline in `osk.js`.
- **Same-class pre-existing gap elsewhere, out of scope.** `osk.js`'s
  `htmx:afterSwap`-adjacent `focusin`→`hide()` deferral (ut-docs#1231) and
  its `focusout` handler use the same short-timeout pattern and share the
  theoretical flaw, currently shielded by real touch's implicit pointer
  capture — worth its own ticket now that this fix deliberately routes a
  new population (mouse-shaped sessions) through the unshielded path.
  Filed as **ut-docs#1306**.
- Screenshot pixel deltas (`sell.png`, `invoices.png`) traced to the fix's
  own toggle-reveal changing residual cursor/hover state during
  `docs-shots.spec.ts`'s staging clicks — cosmetically neutral to
  arguably-improved (Add no longer stuck mid-hover), no manual prose
  invalidated.
- No secrets or real client/shop names introduced (demo-catalog barcode
  and product name already used elsewhere in the suite).

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | empty / pass / pass |
| `go test ./...` | pass (no Go files touched by this diff) |
| `guard-docs-shots.sh` / `guard-i18n.sh` / `guard-help-topics.sh` / `guard-htmx-loaded.sh` / `guard-autofill-suppression.sh` | pass |
| OSK e2e suite (8 specs, 46 tests: `osk-central-guard`, `settings-osk`, `osk-decimal-admin-fields-1275`, `osk-signed-minus-key-1276`, `index-keyboard-1023`, `shifts-tips-osk-1272`, `sale-screen-osk-scan-submit-1177`, `designer-search`) | 46/46 pass |
| `make docs-shots` (92 screenshots × 4 locales) | 92/92 pass |
| TDD re-verification (original bug) | revert `osk.js` to pre-fix → new "plain click" test fails (`inputmode` stays `null`); restore → passes |
| TDD re-verification (deferral/click-vs-pointerdown fix) | swap `click`-only back to `pointerdown`+`setTimeout` → realistic-press test times out waiting for `/api/pos/...`; restore → passes |

## Verdict

Safe to merge. One blocking finding from the independent review was fixed
and re-verified before this commit; the fix's own history (a real race
found and fixed mid-implementation, by design intent to accept a much
narrower one) is recorded above and inline in `osk.js` for the next
reader.
