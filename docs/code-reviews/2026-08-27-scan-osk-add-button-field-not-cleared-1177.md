# Code review: scan-row code field not cleared after an OSK/Add-button submit (ut-docs#1177)

**Date:** 2026-08-27
**Author (pipeline lane):** `lane:cloud-54`, Sonnet dev, independent Opus
review subagent (fresh context)
**Card:** universaltill/ut-docs#1177 ("Sale screen: SKU '30005' failed to
add via touchscreen even though it looks correct — backend resolves it fine
when injected directly")

## What shipped

- `web/public/app.js`: a delegated `document.addEventListener('submit', …)`
  listener on the scan-row form that clears `input[name="code"]` after ANY
  submission of that form (`setTimeout(…, 0)`, matching the existing
  hardware-scanner path's own clear), not only the one already applied by
  the `keydown`-driven `submit()` helper.
- `e2e/tests/sale-screen-osk-scan-submit-1177.spec.ts`: 4 new Playwright
  specs, all driving the real code paths with genuine `click()` events (not
  `.fill()`/keyboard synthesis) — single-scan sanity for both the OSK's own
  `↵` key and a direct tap on the visible "Add" button, plus a
  second-consecutive-scan regression for each.
- `web/help/img/manifest.json`: regenerated surface hash (`make
  docs-shots`) — `app.js` is part of the guard's hashed surface; no `.png`
  changed, confirmed by `git status` showing only the manifest touched.

## Root cause

`app.js`'s hardware/wedge-scanner path (`window.addEventListener('keydown', …)`
→ `submit()`) clears the scan-row's code field after every submit. Two other
ways to submit the same form both bypassed `submit()` entirely and never
cleared it:

1. `osk.js`'s own `↵` key (`press('↵')` → `form.requestSubmit()`).
2. A direct tap on the visible "Add" `<button type="submit">`.

Invisible on the first scan of a fresh page — which is all an SSH-injected
keystroke (a real `Enter` keydown, always taking the hardware path) or a
naive single-scan test ever exercises — but every scan after the first
concatenated the new code onto the stale one, producing a garbled code that
resolves to nothing and surfaces exactly the reported "item not found"
toast. This explains both halves of the original report: the SSH
reproduction "worked immediately" (first scan, hardware path, unaffected),
and the operator's real touchscreen session did not (multiple scans, OSK
path, corrupted after the first).

## How this was found: the first diagnosis was wrong, and review caught it

The initial pass (this same session, same lane) wrote a single-scan-per-page
test for both the OSK `↵` and Add-button paths, both passed, and concluded
theory 1 (OSK Return key doesn't submit) was ruled out — recommending the
card be closed as "the remaining explanation is ut-docs#1170's touch-drag
bug, out of scope here." An independent Opus review subagent (fresh
context, no visibility into the dev reasoning) was run before committing
that conclusion. It:

- Read `osk.js`, `app.js`, and the scan-row form directly rather than taking
  the test's passing state as proof of correctness.
- Noticed the draft test's own comment rationalizing away the leftover
  field value ("a field-clear behavior this form doesn't implement") and
  recognized that as the bug itself, not a design choice — app.js's own
  hardware path *does* clear it.
- Verified live: single-scan value after OSK `↵` = stale code left in field;
  after a second scan, field value became the two codes concatenated; the
  resulting basket showed "item not found," reproducing the report exactly.
- Confirmed via mutation testing that the drafted test suite would still
  pass even with `hide()`/`el.blur()` deleted from `osk.js` (OSK-hidden
  state untested) — a real coverage gap, since fixed (see below).
- Flagged a false-pass risk: no `beforeEach` reset, so a stale line from an
  earlier spec sharing the same server-global basket could satisfy the
  `toContainText` assertions before the scan under test ever ran.
- Corrected an inaccurate CSS claim in the draft (that `.btn` has
  `user-select: none` unconditionally — it only does inside `body.kiosk`)
  and confirmed the click-vs-drag reasoning for scoping the *other* theory
  to ut-docs#1170 was otherwise sound.
- Caught a wrong source citation for the test barcode (attributed to
  `001_init.sql`, which still carries the pre-migration-031 checksum; the
  real source is `demo_catalogue.sql` + migration 031).

All of the above were fixed in this same commit before shipping: the field
now clears after any submit (root fix), the test suite gained
`beforeEach` reset, an explicit "no error toast" assertion, an `#osk`
hidden-after-submit assertion, a second-scan regression per path, and the
correct source citation.

## Verification

- **Mutation-tested against the actual fix**: `git stash`-ing just the
  `app.js` change and re-running the 4 new specs reproduces the exact
  reported failure mode — `expect(input).toHaveValue(code)` fails with the
  two barcodes concatenated (`"20000100000122000010000098"`), all 4 tests
  red. Restoring the fix — all 4 green again.
- `gofmt -l .` clean, `go build ./...` clean (no Go changed).
- `go test ./...` — all packages pass.
- Full e2e suite (`npx playwright test --project=default`, 192 specs):
  **191 passed**, 1 pre-existing failure unrelated to this change
  (`catalog-image-to-till.spec.ts`, already tracked as a sandbox-specific
  flake on ut-docs#1120 — thumbnail `complete=false`, nothing to do with
  the scan form).
- All 18 CI-blocking guards under `scripts/ci/` pass, including
  `guard-docs-shots.sh` after regenerating the manifest (no visual diff —
  only the recorded surface hash moved, confirmed by `git status` showing
  no `.png` changed).

## Disposition of the two original theories

1. **OSK Return key doesn't submit** — this WAS the real bug, just not in
   the way originally framed (it does call `form.requestSubmit()`
   correctly; the defect was the missing field-clear that only bites on a
   second scan). Fixed.
2. **Add-button tap swallowed by touch-drag-selects-text (ut-docs#1170)** —
   correctly out of scope for this card; a Playwright `click()` cannot
   reproduce a touch-drag gesture, so this remains untested here by design.
   Not the primary explanation for the original report (the field-clear bug
   explains it fully on its own, and `body.kiosk .btn` already carries
   `user-select: none` on the real till's actual kiosk deployment), but
   still a real, separate gap tracked under #1170 and not touched by this
   change.

## Not verified here (accepted, tracked elsewhere)

- Real touchscreen hardware — this fix is pure JS event-wiring, verified
  via genuine Playwright `click()` events against a real dev server; no
  physical-Pi-only behavior is involved, unlike ut-docs#1170/#1187.
- WebKit/GTK rendering (`unitill-desktop`'s embedded shell) — this sandbox
  has no GTK/WebKit toolchain (same limitation as ut-docs#1187); the fix
  lives in `app.js`, served identically to any browser rendering the page,
  so no WebKit-specific behavior is expected, but not independently
  confirmed on that engine.

## Merge

Feature branch `fix/1177-osk-scan-submit-regression`, PR references `Closes
universaltill/ut-docs#1177`, `merge_method: "merge"` per ut-docs#250 (never
squash/rebase).
