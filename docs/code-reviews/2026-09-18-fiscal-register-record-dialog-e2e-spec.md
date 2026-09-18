# Code review — e2e spec for /fiscal-register's record_dialog view-mode behaviour (ut-docs#2403)

- **Date:** 2026-09-18
- **Ticket:** universaltill/ut-docs#2403 (`complexity:easy`, `ux`) — coverage-only
  follow-up split out of ut-docs#2186 at that card's own Reviewer close-out
  (deferred there as finding B5).
- **Branch:** `feat/2403-fiscal-register-e2e-spec`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per
  `complexity:easy` routing, `MODEL-ROUTING.md` — the same model built and
  reviewed, but a clean instance that never saw the dev reasoning),
  isolated in its own git worktree.
- **Verdict: SAFE TO MERGE.** No blocking findings.

## What shipped

One new file, `e2e/tests/fiscal-register-record-dialog-2186.spec.ts` — no
product code touched. `/fiscal-register` is the first
`list-and-dialog-pattern.md` adopter where a row opens the shared
`record_dialog` genuinely **read-only** (no update endpoint exists for
these fields — only create + one-directional decommission), and this was
the one adopter shipped with no automated browser coverage. Six tests,
mirroring `registers-record-dialog-2185.spec.ts`'s shape (real Playwright
browser, not element-exists assertions):

1. Row tap opens every field genuinely disabled (proven via `.focus()` +
   `document.activeElement`, not just `.toBeDisabled()`) with Save hidden
   and the view note visible; a real `Enter` on the dialog root does
   nothing; New still opens a live, fully-enabled create dialog.
2. Viewport geometry at 1024×600 and 360px — dialog full-bleed,
   status/lock/exit-to-OS reachable, no page-level horizontal scroll.
3. An entry whose register has since been deactivated still shows the
   correct till name via the synthetic `<option>` fallback (ut-docs#2186
   review finding B3), and that synthetic option doesn't leak into the
   next create-mode open.
4. Decommission is present only while `decommissioned_on` is empty, and
   works from inside the view dialog (one-directional, no Activate
   counterpart).
5. Filtering to zero matches within one location group hides that whole
   group — heading, address form, table header (finding B6) — not just
   its rows; a match anywhere re-shows it.

## What the independent review found, and what was fixed

Nothing blocking. The reviewer actively tried to break two of this file's
own non-obvious design choices rather than take their governing comments
on faith:

- **Test order.** The viewport-geometry loop runs before the group-filter
  test that creates a real stock location. The reviewer mechanically
  reordered the file (group-filter first) and reran: the 360px case then
  failed for real (`scrollWidth - clientWidth = 184`), reproducing a
  genuine, pre-existing bug in this page's own per-location address-edit
  form (`.users-inline`, explicitly "untouched, unconverted" per that
  template's own comment, predating ut-docs#2186 and unrelated to the
  dialog) — confirming the shipped ordering is load-bearing, not
  decorative. Filed separately as **universaltill/ut-docs#2420** rather
  than fixed or silently avoided here (out of scope for a coverage-only
  card; see ut-docs#2403's own non-goal).
- **`fiscalGroupWithText`'s locator.** The reviewer patched it to reuse a
  `.first()`-chained row locator inside `.filter({ has })` and reran: it
  broke immediately ("element(s) not found"), confirming Playwright's
  `.filter({ has })` genuinely needs its own unchained locator here, not
  just a style preference.
- Also independently confirmed: every register/location this file creates
  is uniquely named (`Date.now()`) and never touches the ecosystem's
  normally-seeded fixtures; every mutating helper waits for network idle
  after its htmx-boosted redirect before navigating elsewhere (fixes a
  real, reproduced `net::ERR_ABORTED` race, unrelated to server health,
  found while building this file); `watchConsole(page)` runs in every
  test with no exemption; no hardcoded real client/shop name or
  secret-shaped literal.

## Independently re-verified myself (orchestrator), separate from the review subagent

- `gofmt -l .` clean; `go build ./...` clean; `go test
  ./internal/pages/...` — all packages `ok`, 0 failures (untouched by this
  diff, run as a sanity check since the spec drives real handlers).
- `scripts/ci/guard-e2e-fixtures-import.sh` passes (145 specs checked,
  including this new one, all importing `test`/`expect` from
  `./fixtures`).
- Ran the spec itself twice against a real per-worker till server and
  Chromium (`PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers npx playwright test
  --project=default fiscal-register-record-dialog-2186.spec.ts`): 6/6
  passed both times, no flake.
- Traced the actual root cause of two real bugs hit while building this
  file before they were fixed: an `ERR_ABORTED` navigation race (missing
  `waitForLoadState('networkidle')` after an htmx-boosted redirect) and a
  `.filter({ has })` locator quirk — both reproduced against a live
  manually-run till server via direct HTTP/browser scripts, not guessed.

## Explicitly deferred / follow-ups filed

- **universaltill/ut-docs#2420** — `/fiscal-register`'s per-location
  address-edit form (`.users-inline`) overflows the page horizontally at
  the 360px floor once that location's group has at least one entry; a
  real, pre-existing, out-of-scope bug found while writing this file's
  viewport test, not introduced by it and not fixed here.
