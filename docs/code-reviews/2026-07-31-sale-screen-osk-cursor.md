# Code review — sale screen: suppress auto on-screen-keyboard, hide kiosk cursor, manual toggle

- **Date:** 2026-07-31
- **Task:** ut-docs#155
- **Branch:** `feature/sale-screen-osk-cursor`
- **Author:** pipeline Dev step
- **Independent reviewer:** general-purpose subagent on **Opus** (different model, per standing practice)

## What shipped

1. **Barcode/scan input** (`web/ui/pages/index.html`): `data-osk="off"`
   suppresses `osk.js`'s existing on-screen-keyboard auto-open on this
   autofocused field (the reported bug — it popped open immediately on any
   touch till in the default "auto" mode).
2. **`osk.js`'s `wantsOSK(el)`**: field-level `data-osk="off"` now only
   suppresses **auto** mode — `mode === 'on'` (an admin's explicit global
   "Always on" override, for tills with no physical keyboard at all) still
   wins, on this field like every other.
3. **New `#scan-keyboard-open` toggle button** — click handler removes the
   field's `data-osk="off"`, focuses it (triggering the existing
   `focusin` → `show()` path, no new bypass API), then restores
   `data-osk="off"` on the field's next `blur` — a one-shot deliberate
   open, not a permanent latch.
4. **`app.css`**: `body.kiosk, body.kiosk * { cursor: none; }` (kiosk-only,
   server sets `.kiosk` only under `UT_KIOSK=1`).
5. **i18n**: `tender.scan.keyboard` in all 4 locale files.
6. New e2e file `e2e/tests/sale-screen-osk-cursor.spec.ts` (2 tests, real
   touch-emulated browser context) + one tightened Go assertion in
   `internal/pages/ui_smoke_test.go`.

## Independent review — two real, mid-review fixes; one claim disproven

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | **blocking** | `cursor: none` on `body.kiosk` alone loses to every element's own `cursor: pointer` (`.btn`, `.btn-tile`, `.tab`, …) — inherited value, not cascaded — so the cursor stayed visible over nearly the whole kiosk sale screen (only bare background was actually affected) | **Fixed** — `body.kiosk, body.kiosk * { cursor: none; }`; e2e assertion moved from `body` to a real `.btn` element so a regression here would actually fail the test again |
| 2 | should-fix | Toggle button's `removeAttribute('data-osk')` was permanent — one tap and the field is OSK-eligible forever, so the "New Sale" reset (which refocuses this field every sale) would re-pop the keyboard on every subsequent sale for the rest of the page's lifetime, quietly reintroducing the original bug | **Fixed** — a one-shot `blur` listener restores `data-osk="off"`; covered by a new e2e assertion (submit → `data-osk` back to `"off"`) |
| 3 | claimed blocking, **verified incorrect** | Reviewer asserted `browser.newContext()` doesn't inherit the project's `baseURL`, so `touchPage.goto('/')` in the new e2e test would throw and the test could never actually pass — based on reading vendored Playwright source, not execution (no browsers installed in the reviewer's sandbox) | **Dismissed after independent verification**: ran the actual test (passes, real assertions on `#osk` succeed) and a standalone debug spec logging `page.url()` after `newContext().goto('/')` — resolves to `http://127.0.0.1:8091/`, the real server. `browser.newContext()` DOES inherit `baseURL` in the installed Playwright version (1.61.1) here. No change made. |
| 4 | nit | `ui_smoke_test.go`'s `data-osk="off"` substring check could coincidentally match `<body data-osk="off">` if the global OSK setting were ever off in this fixture, making the assertion pass for the wrong reason | **Fixed** — tightened to `data-osk="off" autofocus`, a fragment only the scan input carries on this page |
| 5 | nit | First e2e test attached `watchConsole` to the default `page` fixture it never navigated — a no-op assertion | **Fixed** — dropped the unused `page` fixture from that test |
| 6 | accepted, deferred | The ⌨ toggle renders unconditionally and is a silent no-op when OSK is globally off or the till isn't touch-capable (`#ai-identify-open` right next to it is the repo's own precedent for conditionally hiding an inert button) | **Accepted for this card** — harmless (no error, just inert), and giving it the same runtime-aware hide/show as the AI-identify button means duplicating osk.js's own touch/mode detection or exposing a second query surface from it; logged as new Backlog card ut-docs#176 rather than scope-creeping this fix |

Also independently confirmed clean: `mode !== 'on'` has no staleness edge
case (`settings.html`'s OSK form does `hx-on::after-request="window.location.reload()"`,
so the once-read `var mode` is always fresh after a mode change); the scan
input is the only field-level `data-osk="off"` in the repo, so the relaxed
condition can't accidentally re-enable OSK elsewhere; RTL-safe (`.scan-row`
is plain flex/gap, no left/right, no new CSS for the button); i18n keys are
real translations in all 4 locales, not English copies; no SQL, no file
writes, no cwd-relative paths, no raw user-input rendering, no real
shop/client names anywhere in the diff.

## TDD evidence (independently re-verified, not just claimed)

- `TestIndexAndBasketRender`: written first, confirmed failing pre-fix
  (`data-osk="off"` / `id="scan-keyboard-open"` absent), passing after.
  Reviewer independently re-stripped `data-osk="off"` from the template and
  re-ran — failed with the same assertion, confirming it isn't a tautology.
- `e2e/tests/settings-osk.spec.ts` (pre-existing, 2 tests protecting the
  admin "Always on" override): broke on the first implementation pass
  (unconditional `data-osk="off"` blocked even the forced-on override),
  caught by this pipeline's own Tester step, fixed via the `mode !== 'on'`
  condition, re-verified green — this is exactly the regression class an
  independent review step exists to catch, caught here before it ever
  reached Review.
- Toggle button's first implementation (`window.utOSK.open()` bypass API)
  opened the keyboard but osk.js's own `focusout`→`hide()` safety logic
  closed it ~50ms later (focus had moved to the button, field was still
  `data-osk="off"`) — caught by the new e2e test timing out on the
  subsequent key click, root-caused, and replaced with the
  `removeAttribute` + `focus()` approach that works with the existing
  focusin/focusout machinery instead of against it.

## Verified beyond automated tests

- Full `default`-project e2e suite (`e2e/tests/`, 18 specs) run twice
  (before and after the review fixes): 17/18 pass both times. The one
  failure, `catalog-image-to-till.spec.ts` (an image-load timing
  assertion), reproduces identically with this entire change stashed out
  against unmodified `origin/main` — confirmed pre-existing and unrelated,
  not this card's to fix.
- Full `go test ./...`: all green except
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, which
  fails identically on unmodified `origin/main` in this sandbox (running as
  root — a "read-only directory" doesn't block root's writes here) —
  confirmed pre-existing and environment-specific via `git stash`, not
  introduced by this change.
- `go build ./...`, `go vet ./...`, `bash scripts/ci/guard-i18n.sh`,
  `bash scripts/ci/guard-data-access.sh` — all clean.

## Verdict

**Safe to merge.** One blocking finding and one should-fix finding both
fixed and re-verified; one claimed-blocking finding was independently
re-verified and found incorrect (documented above rather than "fixed"
against nothing); remaining nits fixed; one item accepted and carded
(ut-docs#176) as genuinely out of this card's scope.
