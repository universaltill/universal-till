# Code review — bug-report panel: a close must stick (ut-docs#394)

- **Date:** 2026-08-07
- **Branch:** `fix/394-bugreport-panel-close-persists`
- **Ticket:** universaltill/ut-docs#394 — "Bug-report panel is pinned to the
  bottom of the till and cannot be closed" (field report)
- **Reviewer:** independent review (did not write the implementation)

## What shipped

Root cause: `/report-issue` (`internal/pages/issue_report_page.go`) stamps
`openBugReportPanel: true` on **every** visit, and the panel partial
unconditionally honoured that server-side `open` class. The app does full page
loads for every navigation (no `hx-boost`), so nothing carried a prior explicit
close across a navigation. Re-landing on `/report-issue` — a re-tapped `/menu`
tile, a kiosk reload — popped the panel back open with no way to make it stay
shut, exactly the field report's "opens and never gets closed".

The fix is client-side and deliberately leaves the server stamping alone, so the
no-JS first paint is unchanged:

- `web/ui/partials/bugreport_panel.html` — a `sessionStorage` dismissal flag
  (`ut-bugreport-dismissed`), modelled on the existing `ut-cursor-mouse` flag in
  `web/public/cursor.js`. `closePanel()` sets it; `openPanel()` clears it. Where
  the IIFE used to unconditionally run `initCapture()` for the server-forced-open
  case, it now removes the `open` class (and syncs the toggle) when the flag is
  set instead.
- `e2e/tests/bugreport-panel.spec.ts` and
  `tests/e2e/tests/bugreport_panel.spec.ts` — a mirrored regression lock in both
  Playwright suites: open → close → `goto('/report-issue')` → still hidden →
  explicit re-open works.

## Findings

### Fixed in this branch

**1. `/report-issue` told the operator a lie after a dismissal (user-visible).**
The page body's only content is `issuereport.route_note`, which read *"The
report panel is open. You can also open it from any screen with the 🐞 button in
the top bar."* With the fix in place and a dismissal set, that page renders the
sentence **"The report panel is open."** with the panel closed. Verified live,
not by reading: driving Chromium to `/report-issue` after a close produced a page
whose entire visible body was that false claim.

The same path is what the manual documents — ☰ Menu → 🐞 *Report an issue* — so
after one dismissal the documented tile landed on a page that neither opened the
reporter nor explained why.

Fix: made `issuereport.route_note` state-neutral in all four locales
(`en`/`ar`/`fa`/`tr`) by dropping the leading "the panel is open" claim:
*"You can open the report panel from any screen with the 🐞 button in the top
bar."* This is a value-only change — no new keys — so `guard-i18n.sh` stays
green, and the sentence is now true in both states. The tile still lands
somewhere useful: a page that tells the operator exactly how to open the panel.

**2. The regression tests did not cover the branch the fix creates.**
The suppression branch is the one page state where the panel can exist *un-wired*
— it deliberately skips the lazy `initCapture()` the forced-open path used to
run. Both new tests asserted the re-opened panel was *visible*, which would still
pass if it came back with dead buttons. Strengthened both specs to drive a real
save after the re-open (fill note → Save → "Saved").

Verified beforehand that the panel is in fact functional on that path, so this
locks working behaviour rather than papering over a bug.

**3. Stale comment.** The mirrored lock in `tests/e2e/tests/bugreport_panel.spec.ts`
said "on the first till setup"; that file is the *second* till setup (`:8080`),
as the sibling comment 25 lines above correctly states. Corrected.

**4. Manual not updated.** The change alters what a shop owner experiences —
closing the panel now sticks for the rest of the session, and the *Report an
issue* page no longer forces it back open. Per the standing product-owner
instruction (ut-docs#324) that ships in the same branch. Updated
`web/help/{en,ar,fa,tr}/bug-reporting.md`. No screenshot regeneration: the
screens themselves are pixel-identical, only *when* the panel auto-opens changed.

### Accepted as-is

- **`dismissed()` mirrors `cursor.js` faithfully.** Same `flag(v)` shape, same
  `try`/`catch` swallowing storage-blocked browsers, same `'1'` sentinel, same
  `ut-` key prefix. A storage-blocked till degrades to today's per-page
  behaviour rather than throwing. Consistent; nothing to flag.
- **`sessionStorage` across `page.goto()`.** Not a test-harness artifact: per
  spec the session storage area is keyed to the top-level browsing context +
  origin and survives same-origin navigations for the tab's lifetime. Playwright
  gives each test a fresh context, so there is no cross-test leakage either —
  confirmed by the second suite running `fullyParallel` with 2 workers and still
  passing. The real-world semantics are the intended ones: dismissal lasts the
  operator's session, a fresh tab/session starts clean.
- **Non-modal guarantees intact.** The diff adds no backdrop, no focus trap and
  no document-level key handler; the only document-level listener is the
  pre-existing delegated click on `#bugreport-toggle`. The non-modal e2e tests
  (full sale completes underneath, page scrolls underneath, typing underneath)
  all still pass.
- **Manager gating untouched.** No change to `isManagerOrAuthOff` or the chip
  route; the flag is a client-side display preference and gates nothing.
- **No disk, no SQL, no money.** The diff touches only `.html`, `.ts`, `.json`
  and `.md` under `web/`, `e2e/` and `tests/`. Zero Go changes, so neither of the
  two recurring bug classes this pipeline keeps finding can apply. Checked the
  neighbouring code anyway: `issuereport.Save` does call `os.MkdirAll`, and
  `PendingDir`'s cwd-relative default is overridden to
  `paths.Data("issue-reports", "pending")` in `internal/pages/init.go`.
- **Existing behaviour preserved.** `TestReportIssuePage_RendersPanelOpen` and
  `TestBugReportPanel_ClosedByDefault` are server-side only and unaffected; the
  pre-existing e2e test "/report-issue still works and lands with the panel
  already open" still passes, because a fresh session has no flag set.

### Out of scope — new Backlog cards, not acted on here

- **Chip `aria-expanded` is stale when the panel is server-opened.**
  `bugreport_chip.html` hardcodes `aria-expanded="false"` and arrives via htmx
  *after* the panel's inline script has run, so nothing re-syncs it. Confirmed
  live: on a fresh `/report-issue` the panel is visible while the toggle reports
  `aria-expanded="false"`. Pre-existing (predates this diff); the fix's
  suppression branch happens to make the *dismissed* case correct. A proper fix
  needs the chip to read panel state or an `htmx:afterSwap` sync.
- **Local e2e runs can silently test a stale binary.** `e2e/playwright.config.ts`
  sets `reuseExistingServer: !process.env.CI`, and templates/locales are
  `go:embed`ed — so a server left running from an earlier run serves the *old*
  embedded assets and Playwright reuses it without rebuilding. This produced a
  false "the test passes without the fix" result during this review (see below).
  CI sets `CI=1` so CI is unaffected, but a local TDD claim can be silently
  wrong. Worth a `pkill` in `run-till.sh` or a build-stamp healthcheck.
- **`data/*.db-shm` / `data/*.db-wal` aren't gitignored.** `.gitignore` covers
  `data/*.db` only, so an e2e run leaves `?? data/` dirty in `git status`.

## Verified personally (not taken on trust)

- **The TDD claim, re-verified — and it initially failed to reproduce.** First
  attempt: `git stash push -- web/ui/partials/bugreport_panel.html`, ran the new
  test, and it **passed**, which would have meant the test locked nothing. Cause
  was not the test: two till servers from an earlier run (PIDs 15553 and 28487,
  built *with* the fix embedded) were still listening, and `reuseExistingServer`
  reused them. After killing both and forcing a rebuild, the test failed exactly
  as claimed:
  `expect(locator).toBeHidden() failed … resolved to <section class="bugreport-panel open" …> unexpected value "visible"` at the `goto('/report-issue')` assertion.
  `git stash pop` → 10/10 pass. The other 9 tests in the file passed in both
  states, so the new test is the only thing the fix moves.
- **Full Go gate:** `go build ./...` clean, `go vet ./...` clean,
  `go test ./...` — one failure only, `TestSaveCleansUpDirectoryOnWriteFailure`
  in `internal/issuereport`. Confirmed environment-only and pre-existing: the
  test pre-creates a `0o500` directory expecting the write to fail, and this
  sandbox runs as uid 0, where mode bits don't stop root. `git status internal/`
  is clean — this diff does not touch that package.
- **Guards:** `guard-data-access.sh`, `guard-i18n.sh` (842 template keys resolve,
  all locales match `en.json`) and `guard-help-topics.sh` (route coverage, all
  shipped locales complete) all pass after the locale and manual edits.
- **Both Playwright suites, driven for real** against a pre-installed Chromium:
  - `e2e/` default project: **66 passed, 1 failed**. The failure is
    `catalog-image-to-till.spec.ts`, which I confirmed **also fails on the
    stashed clean tree** (baseline: 65 passed, 1 failed — same test) — pre-existing,
    unrelated to this diff. An earlier run also showed `inventory-to-till`
    failing; it passes on re-run and on the clean tree, so it is a flake from a
    server killed mid-boot, not a regression.
  - `tests/e2e/`: **17 passed, 4 skipped** (the skips need `DOCS_ROOT`),
    including all 5 bug-report panel tests.
  - The sandbox `executablePath` override needed to launch Chromium here was
    temporary; `e2e/playwright.config.ts` and `tests/e2e/playwright.config.ts`
    are clean in `git status` and are **not** part of this commit.
- **Drove the actual failure mode by hand**, not just via assertions: dismissed
  the panel, navigated back to `/report-issue`, and read the rendered page text
  — which is how finding #1 (the "The report panel is open." contradiction) was
  found, and how the corrected copy was confirmed afterwards.
- **Drove the re-open path by hand:** after a dismissal, re-opening via 🐞 gives
  `aria-expanded="true"`, a visible panel, and a save that reaches
  `/api/issue-reports` and reports "Saved" — no dead-button state.
- **Secrets / client names:** scanned the diff for credentials, tokens, keys and
  real customer or company names. None; the only literals added are the
  `ut-bugreport-dismissed` storage key and e2e test note strings.

## Verdict

**Safe to merge.** The root-cause analysis is right, the fix is the minimal
client-side change that leaves the server-side no-JS first paint intact, and the
regression is genuinely locked in both suites (personally re-verified failing
pre-fix). The one real defect the original diff carried — `/report-issue`
asserting "The report panel is open." while it was closed, on the very path the
manual documents — is fixed here along with the manual and the missing
functional assertion. Remaining failures in the tree are pre-existing and
reproduce on `main`.
