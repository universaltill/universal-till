# Code review: exit-to-OS 429 shows "locked out", not "wrong PIN" (ut-docs#1104)

**Branch:** `fix/1104-exit-to-os-lockout-message` (commits `d2a7512`, `8bd48a5`)
**Reviewer:** independent fresh-context Sonnet pass (complexity:easy →
Sonnet-reviews-Sonnet in a clean-context subagent, per scrum-master's model
routing) · **Author:** Sonnet (this pipeline cycle)

## What shipped

`POST /api/settings/exit-to-os` already answered `429` for the device-wide
manager-PIN lockout (`auth.ErrLockedOut`), separately from the plain-wrong-PIN
`403` — but both client forms that call it funneled every non-2xx, non-503
status into the same generic "Incorrect PIN or not authorized." message. A
locked-out operator (possibly locked out by *someone else's* failed keypad
attempts) was told their PIN was wrong, on the login screen specifically the
one screen reached only when already stuck on something. This is exactly
finding 3 flagged (not fixed, deferred as a follow-up card) in
`docs/code-reviews/2026-08-26-exit-to-os-login-escape-1099.md`.

- **`web/ui/pages/settings.html`** and **`web/ui/pages/login.html`** — the
  exit-to-os fetch handler now special-cases `r.status === 429` and renders
  the existing `auth.error.locked` copy ("Too many attempts — wait 30
  seconds") instead of falling through to the generic error. No new i18n key
  — reused the one the main PIN pad's own lockout path already uses.
- **`internal/pages/settings_page_test.go`** —
  `TestExitToOSEndpoint_LockedOutReturns429NotGeneric403`: burns the
  device-wide lockout with 5 wrong, non-blank `manager_pin` attempts, then
  asserts the 6th attempt — even with the *correct* PIN — comes back 429, not
  403. Closes a real, pre-existing server-side coverage gap: the only prior
  test touching this budget
  (`TestExitToOSBlankPINRejectedWithoutBurningLockoutBudget`) deliberately
  used blank PINs, which never reach `AuthorizeManager`, so the real lockout
  path had zero coverage.
- **`e2e/tests/exit-to-os-lockout-1104.spec.ts`** — two Playwright cases
  against `/settings` (default, auth-off project), mocking a 429 and a 403
  response via `page.route()` and asserting the rendered `.pos-notice` text
  in each case, plus the mirror-image negative assertion (429 doesn't show
  the generic text, 403 doesn't show the lockout text).
- **`web/help/img/en/invoices.png`** + `manifest.json` — `make docs-shots`
  regen noise (the surface hash covers all of `web/ui/**` +
  non-test `internal/pages/**.go`, so any change in that surface reruns every
  topic's screenshot).

**Explicitly documented gap, not silently skipped:** `login.html`'s identical
branch is not separately driven by e2e — only `settings.html`'s is. Exercising
`login.html`'s form needs the `auth` Playwright project's real
first-boot/login harness, judged disproportionate for this card given the
branch is code-identical to `settings.html`'s (both reviewed line-by-line
below).

## Verification (re-run personally, not taken on report)

- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean.
- `go test ./internal/pages/... -run 'TestExitToOSEndpoint' -v` — all 6 cases
  in that group pass, including the new one.
- Full `go test ./internal/pages/...` (no `-race`, per ut-docs#1119's known
  near-timeout issue with this package under `-race`) — clean,
  `ok … 100.926s`.
- `go test ./internal/pages/... -race -run 'TestExitToOSEndpoint' -v` — clean,
  no race reported, ~7s.
- **TDD re-verification, Go side:** the new Go test isn't a regression test
  for a bug this diff introduces or fixes — the handler's 429/403 mapping
  (`internal/pages/settings_page.go:859-866`) pre-dates this branch entirely
  and is untouched by it. It's honest new coverage of pre-existing behaviour.
  Confirmed it passes on its own merits; there is no meaningful "pre-fix"
  state of this specific file to revert to.
- **TDD re-verification, e2e side (revert → run → restore, atomically within
  this isolated worktree, never on a shared checkout):**
  - `git checkout main -- web/ui/pages/settings.html web/ui/pages/login.html`
  - Installed e2e deps (`npm install` in `e2e/`) and, since this
    environment's pre-installed Chromium (`/opt/pw-browsers/chromium-1194`)
    predates the pinned `@playwright/test@^1.48.0`'s expected revision, used a
    throwaway, never-committed config
    (`e2e/.review-pw-override.config.ts`, extending the real config with
    `use.launchOptions.executablePath` pointed at that binary) — deleted
    afterward, confirmed `git status` clean.
  - Ran `npx playwright test --config=.review-pw-override.config.ts
    exit-to-os-lockout-1104 --project=default` against the **fixed** code
    first: both cases pass (baseline).
  - Reverted the two files to `main`'s pre-fix content, re-ran: the 429 case
    fails exactly as predicted —
    `Expected: "Too many attempts — wait 30 seconds" / Received: "Incorrect
    PIN or not authorized."` — while the 403 case still passes unaffected
    (proving the two branches are independent, not coupled).
  - `git checkout HEAD -- web/ui/pages/settings.html web/ui/pages/login.html`
    restored the fix; re-ran: both cases green again.
  - No lingering `till` server processes after the run (`ps aux` clean); temp
    Playwright artifacts (`test-results/`, `playwright-report/`,
    `node_modules/`, the throwaway config) removed, working tree clean.
- **`r.status === 429` overload check** (`internal/pages/settings_page.go`):
  read the whole handler — 429 is written in exactly one place
  (`if errors.Is(err, auth.ErrLockedOut) { status = http.StatusTooManyRequests }`),
  nowhere else in this handler. Grepped every other `StatusTooManyRequests`
  use in `internal/pages/**` (`api_gates.go`'s `rateLimited` wrapper,
  `pairing_api.go`, `refund_page.go`, `shifts_api.go`) — all are separate
  handlers on separate routes (`/api/sync/*`, `/api/setup/*`, refunds,
  shifts); none wraps or shares code with `/api/settings/exit-to-os`. No
  middleware in this codebase globally rate-limits all routes. Confirmed: a
  429 from this specific endpoint can only mean the device-wide PIN lockout.
- **Lockout-threshold arithmetic** (`internal/auth/service.go`): confirmed
  `maxFailedAttempts = 5`, and `recordFailure()` sets `lockedTo` once
  `s.failures >= maxFailedAttempts` — i.e. the *5th* failure trips the lock.
  The new test's "5 wrong attempts, 6th even-correct attempt gets 429" is
  exactly right; not off-by-one in either direction.
- **`auth.error.locked` string read in all four locales** (en/ar/fa/tr):
  "Too many attempts — wait 30 seconds" / Arabic / Farsi / Turkish
  equivalents — all read sensibly reused for exit-to-os. None of the strings
  name a specific screen or mechanism ("PIN pad", "keypad"), they just state
  the fact ("too many attempts, wait"), which is equally true and equally
  actionable regardless of which form triggered it. No real ambiguity;
  minting a second, near-identical key here would be duplication for no
  reader benefit.
- **e2e console-exemption regex scoping** (`e2e/tests/helpers.ts`'s
  `watchConsole`): `extraExempt` is passed per-test-call and only alive for
  that one `page.on('console', …)` listener registered inside that test's own
  scope — it cannot leak into or swallow errors from a different test. Within
  a single test, only one network call (the mocked exit-to-os fetch) can
  produce a "Failed to load resource" line matching `429`/`403`, so no other
  real console error is plausibly caught by these two patterns in this file.
- **`invoices.png` regen "noise" verified, not assumed:** extracted both the
  `main` and `HEAD` blobs and diffed with Pillow (`ImageChops.difference` +
  `getbbox()`) — `bbox is None`, i.e. **pixel-identical**; the 6-byte file
  size delta is PNG re-encoding variance from a second `make docs-shots` run,
  not a rendering change. `guard-docs-shots.sh` independently reports the
  manifest fresh.
- Guards run directly: `guard-i18n.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-compliance-claims.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-htmx-loaded.sh` — all pass. (The remaining
  CI-blocking guards were not re-run individually; this diff touches no
  Android/webkit/emoji-font/autofill/brand-asset/Makefile surface, so they
  are not expected to be affected and were skipped rather than run
  needlessly.)

## Findings

### 1 · Confirmed clean — branch ordering and fallthrough correctness

Both forms check 429 after the `r.ok`/503 branches and before the generic
`else`, so a 429 can never be shadowed by an earlier check nor swallow a
different status. Verified byte-for-byte identical branch shape between
`settings.html` and `login.html` (same comment content, same variable names,
same order) — no copy-paste drift between the two mirrors.

### 2 · Confirmed clean — scope of the diff

No markup change beyond the JS branch and the `T` lookup-object addition in
either file; no accidental structural/CSS change riding along.

### 3 · Accepted scope cut — `login.html`'s branch is code-reviewed but not
separately e2e-covered

Agreed as acceptable for this card. The branch is genuinely code-identical to
`settings.html`'s (same status check, same key, same fallthrough order), the
server-side 429 mapping the whole thing depends on is exercised by the new Go
test regardless of which page calls it, and standing up the `auth` project's
real first-boot/login harness for one already-reviewed, pattern-identical
branch is a disproportionate lift for what CLAUDE.md and the card both frame
as an "easy" wording fix. Not fixed, not deferred as a new card — this is a
one-time judgment call the diff itself already documents honestly in the
commit message, and I concur with it.

### 4 · Confirmed clean — manual / help-topic threshold

`web/help/en/display.md` (step 9) documents the exit-to-os action's
*existence* and its already-existing failure states ("if the window can't be
reached, it tells you so and nothing changes") but doesn't quote or promise
any specific error string. This diff changes what message displays after an
existing, already-documented failure mode — it doesn't add a new screen, step,
or control, and it doesn't make anything the manual currently says false.
Below the "anything a shop owner sees or does" bar that requires a manual
edit in the same branch; `guard-help-topics.sh` agrees (no topic references
this wording). No manual change needed, and none made.

### 5 · Confirmed clean — no secrets, no real client data

Test PINs (`482913`, `000000`) are synthetic fixtures, consistent with the
rest of this test file's convention (`newFullAuthDeps`, `cashUser`). No real
shop/client name anywhere in the diff.

## Confirmed clean (checked, no change needed)

- **429-overload risk** — see Verification above: 429 on this endpoint is
  exclusively `auth.ErrLockedOut`, never any unrelated rate limiter.
- **i18n rule compliance** — no new hardcoded strings; the reused key is
  already present and identical across all four locale files (no drift
  introduced), routed through the page-local `var T = {…}` pattern CLAUDE.md
  prescribes for inline `<script>` blocks.
- **RTL** — no new markup or CSS; the message renders through the pre-existing
  `renderNotice`/`.login-error` machinery, already logical-property-safe from
  ut-docs#1099's review.
- **File-write / path-safety recurring bug classes** — N/A. This diff writes
  no files and touches no filesystem paths; no `os.MkdirAll` or
  `paths.Data(...)` opportunity exists here.
- **Repository-pattern / data-access rule** — untouched; no SQL added
  anywhere, `guard-data-access.sh` passes.
- **Kiosk isolation rule** — untouched; `/api/settings/exit-to-os` isn't a
  `/self-order` route, `guard-kiosk-engine.sh` passes regardless.

## Verdict

**Approve — safe to merge.** No blocker found; no code change needed from
this review (the diff as authored is correct and complete for its stated
scope). Both the new Go coverage and the e2e regression claim were
independently re-verified — the Go test passes on its own merits (honest new
coverage, not a revert-provable fix), and the e2e case was proven to fail with
the exact predicted message-swap against the pre-fix JS, then to pass again
once restored, using a real Chromium instance in an isolated worktree. The
`login.html`-not-e2e-covered scope cut is accepted as reasonable given the
line-for-line-identical branch already reviewed here. The `invoices.png`
regen diff was verified pixel-identical rather than assumed inert.

Nothing deferred as a new backlog item — the one open item from the diff's
own commit message (login.html's e2e gap) is a judgment call accepted above,
not a bug.
