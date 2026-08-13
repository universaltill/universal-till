# Code review: `make docs-shots` runnable in cloud pipeline sessions (ut-docs#622)

## What shipped

`make docs-shots` previously always ran `npx playwright install --with-deps
chromium` before capturing the manual's screenshots — a network download a
cloud pipeline session cannot make (and is instructed not to attempt), so
the target was simply unrunnable there, and `scripts/ci/guard-docs-shots.sh`
then blocked any change touching `web/ui/**`, `web/public/**` or
non-test `internal/pages/**.go`, visual or not.

New `e2e/scripts/`:
- `resolve-chromium.sh` — tries `$PLAYWRIGHT_CHROMIUM_EXECUTABLE`, then
  `$PLAYWRIGHT_BROWSERS_PATH/chromium`, then a fixed fallback
  (`/opt/pw-browsers/chromium`, the cloud session's pre-installed path).
  Each candidate is smoke-tested by actually launching it
  (`smoke-launch.js`) rather than trusted on path existence alone. On a
  resolved candidate it also compares the browser's real version (from the
  smoke launch, via CDP) against what the current `@playwright/test` pin
  actually expects (`expected-chromium-version.js`, read from
  `playwright-core`'s own `browsers.json` — never hardcoded) and prints a
  loud, non-fatal warning to stderr when they diverge.
- `smoke-launch.js` — launches a given executable under Playwright, renders
  a page, prints the browser's version on success.
- `expected-chromium-version.js` — reads the `chromium` entry's
  `browserVersion` straight from `playwright-core/browsers.json` on disk
  (its package `exports` map blocks a plain `require()` of that path, so
  this resolves the package directory and reads the file directly).
- `docs-shots.sh` — the target's actual work, extracted from the Makefile:
  `npm ci`, ask `resolve-chromium.sh` for a usable browser; if found, skip
  `playwright install` and set `PLAYWRIGHT_CHROMIUM_EXECUTABLE`; otherwise
  run the original `playwright install --with-deps chromium` unchanged.
  Then the Playwright docs suite and `write-manifest.js`, as before.
- `resolve-chromium_test.sh` — regression suite (matches this repo's
  existing `scripts/ci/*_test.sh` convention), self-skipping (exit 0, no
  failures) when this environment has no pre-installed Chromium or no
  `node_modules/playwright-core` yet.

`Makefile`'s `docs-shots` target now just calls `docs-shots.sh`.
`e2e/playwright.docs.config.ts` adds `launchOptions.executablePath` from
`PLAYWRIGHT_CHROMIUM_EXECUTABLE` when set — a no-op on any machine that
took the normal install path.

## Review

Independent review via an Opus subagent, isolated in a separate git
worktree (own copy of the WIP commit) so its revert/rerun steps couldn't
touch this checkout — `complexity:medium`, so review runs at the stronger
model per the `scrum-master` skill's model-routing table. Verdict: **safe
to merge, no blockers**; found 3 real should-fix issues, all fixed
below, plus 2 nits (one fixed, one deferred as a follow-up card) and one
dead-code nit (fixed).

### Should-fix, addressed

1. **AC #2 ("cannot drift apart silently") wasn't actually met** — the
   first draft smoke-tested launchability only, which says nothing about
   *which* browser build is in use. Reviewer quantified the real gap in
   this environment: pre-installed Chromium is 141.0.7390.37 (rev 1194),
   the `@playwright/test@1.61.1` pin expects Chrome for Testing
   149.0.7827.55 (rev 1228) — 8 majors apart, a different vendor build —
   and `guard-docs-shots.sh` never hashes PNG bytes, only source + PNG
   *existence*, so a screenshot rendered 8 majors off the pin would commit
   silently. Fixed: `resolve-chromium.sh` now compares the resolved
   browser's real version against `playwright-core/browsers.json`'s own
   `browserVersion` (zero new hardcoded constants — tracks whatever the
   installed `@playwright/test` pin actually expects) and prints a loud,
   clearly-bannered stderr warning on divergence. Deliberately **not**
   fatal: failing here would reintroduce the exact "cannot run at all"
   problem this card exists to fix. That trade-off (visible-but-not-fatal
   drift, because the alternative is unrunnable-in-a-cloud-session) is
   recorded here rather than left implicit in a code comment, per the
   reviewer's ADR-0007 note — judged not to need a full ADR (an
   operational/tooling trade-off, not a product architecture decision),
   but written down explicitly for that reason.
2. **`resolve-chromium_test.sh` failed silently in a cold checkout.**
   Case 1's command substitution had no `|| true`, so under `set -e` a
   failed resolve (e.g. `node_modules/playwright-core` not yet installed)
   aborted the whole suite before any `fail()` call printed anything —
   reviewer reproduced exit 1 with zero output in a fresh worktree. Fixed:
   `|| true` added to every case, and the skip guard now also checks for
   `node_modules/playwright-core` (not just the Chromium binary) before
   running any case, printing a clear `skip:` line instead of dying quietly.
   Verified: temporarily moved `node_modules` aside and confirmed the
   suite now prints the skip message and exits 0.
3. **The old Case 3 didn't test what it claimed.** It asserted the same
   final-resolved-path outcome as Case 2, and the reviewer proved this by
   deleting the `[ -x "$c" ]` guard entirely and watching the suite stay
   green — the only externally observable signal (the final resolved path)
   is identical whether the guard skips a candidate outright or the
   candidate is attempted and fails its smoke test. Fixed by removing the
   case rather than keeping a test that doesn't test its own claim, with a
   comment recording why and what was tried.

### Nits

- Fixed: `UT_DOCS_SHOTS_FALLBACK_CHROMIUM` is now inert unless
  `UT_DOCS_SHOTS_TEST=1` is also explicitly set, removing the (low-risk
  but real) path where an accidentally-exported env var could silently
  redirect a production run. Covered by a new test case.
- Fixed: `smoke-launch.js` was mode 644 (harmless — always invoked via
  `node`, never executed directly — but inconsistent with the other
  scripts); now 755, matching the rest of `e2e/scripts/`. Removed a dead
  `pass=0` variable in the test file (never read).
- Deferred as a follow-up card (`ut-docs#632`): the resolver only ever
  looks for a `chromium` binary (full Chrome), but a normal
  `playwright install` defaults to `installByDefault: true` on
  `chromium-headless-shell` too, and a default headless launch actually
  runs the headless shell, not full Chrome — so the reused-vs-installed
  paths already diverge on browser *variant*, not just version. Worth
  narrowing, but it's a second, independent axis from this card's actual
  defect and didn't block AC #1/#2 being met.

## Verified

- `bash e2e/scripts/resolve-chromium_test.sh` — all cases pass (twice: once
  in the review's isolated worktree, once here after the should-fix
  patches, plus the cold-checkout skip path confirmed by moving
  `node_modules` aside).
- **`make docs-shots` run to completion end-to-end, twice** (once before
  the should-fix patches, once after) — 68/68 Playwright screenshot tests
  passed both times, no network browser download attempted, no manual
  workaround. This is the actual AC #1/#3 verification the ticket asked
  for ("verified by an actual cloud cycle regenerating a screenshot, not
  only locally") — this session *is* that cloud cycle. The reviewer
  independently ran it a further three times in their own worktree.
- Confirmed the version-mismatch warning fires correctly against this
  environment's real skew (141.0.7390.37 reused vs. 149.0.7827.55
  expected) and stays on stderr only — `docs-shots.sh`'s
  `chromium="$(... || true)"` capture (stdout only) is unaffected.
- `web/help/img/manifest.json`'s `surface_sha256` was byte-identical
  before/after both docs-shots runs, confirming this diff touches none of
  the hashed product surface (`web/ui/**`, `web/public/**`,
  `internal/pages/**.go`) — expected, since only `e2e/`/`Makefile`/tooling
  changed. The 9 PNGs that came back with small binary diffs on each run
  (`alerts`/`designer`, and `sell` for some locales) were deliberately
  **not committed** — confirmed genuinely non-deterministic (reviewer
  cross-referenced `docs/code-reviews/2026-08-09-bkp-catalog-import-511.md`,
  which already documents `designer`'s wall-clock-baked flakiness as a
  known, accepted pre-existing issue) and out of scope for this card.
  `sell`'s divergence for the `ar`/`fa` locales looked more environment-
  correlated than purely flaky on closer inspection — folded into the
  `ut-docs#632` follow-up rather than treated as unrelated noise.
- `bash scripts/ci/guard-docs-shots.sh` passes (both before and after the
  fix — the guard was never actually blocking on this diff, since it only
  hashes source and checks PNG existence, not pixel content).
- `go build ./...`, `go test ./... -race` (full suite), and all four
  `scripts/ci/guard-*.sh` scripts (data-access, kiosk-engine,
  plugin-menu-read, i18n) pass — none of this diff touches Go source, so
  this is a baseline confirmation per the repo's "before committing"
  checklist, not a meaningful new signal.
- No real client/shop name, no literal credential/secret anywhere in the
  diff. Not a UI-surface change (no `internal/pages`/`web/` template/CSS
  touched) — `reference/ux-guidelines.md` checklist doesn't apply. Not a
  shop-owner-visible behavior change — no `web/help/` manual topic owed.

## Safe-to-merge verdict

Yes. No blockers found or remaining; all should-fix findings addressed
and re-verified; one low-priority nit deferred to `ut-docs#632` rather
than expanding this card's scope.
