# Code review — ut-docs#632: `resolve-chromium.sh` headless-shell variant

**Date:** 2026-08-24
**Card:** [ut-docs#632](https://github.com/universaltill/ut-docs/issues/632)
**Complexity:** easy
**Build model:** Sonnet (inline) — **Review model:** Sonnet, fresh-context subagent (per the easy-tier routing: an independent instance is the "different model" for this tier)

## What shipped

`e2e/scripts/resolve-chromium.sh` (ut-docs#622's pre-installed-Chromium
reuse path for a cloud pipeline session, network-restricted from running
`playwright install`) only ever looked for the full `chromium` binary. But
`playwright-core/browsers.json` installs `chromium` and
`chromium-headless-shell` as two independent, both-`installByDefault`
entries, and a normal fallback headless Playwright launch — no
`PLAYWRIGHT_CHROMIUM_EXECUTABLE` override, no `channel` — actually runs
`chromium-headless-shell`. So the reused-vs-normally-installed paths
diverged on browser *variant*, not just the already-flagged *version* axis.

Fix, scoped to the three files the ticket named:

- `resolve-chromium.sh`: after the explicit `PLAYWRIGHT_CHROMIUM_EXECUTABLE`
  override (unchanged, still tried alone first — absolute precedence), now
  prefers a pre-installed `chromium-headless-shell` candidate before falling
  back to full `chromium`. Headless-shell has no stable convenience symlink
  the way `chromium` does, so its path is revision-globbed
  (`chromium_headless_shell-*/chrome-linux/headless_shell`) rather than
  fixed, mirroring the existing `PLAYWRIGHT_BROWSERS_PATH`-then-real-default
  candidate-list shape for the full build.
- `expected-chromium-version.js`: takes an optional browser-entry-name
  `argv[2]` (default `"chromium"`), so the version-mismatch warning compares
  a resolved candidate against the `browsers.json` entry matching its actual
  variant, not always `"chromium"`'s.
- `resolve-chromium_test.sh`: new/updated cases cover the preference order,
  fallback to full `chromium` when headless-shell is unavailable, and the
  entry-name arg (see finding below). Verified TDD-style against the
  pre-fix script (`git show HEAD~1:...`) that every new/changed assertion
  actually fails without the fix — not tautological.

## What the independent review found

Spawned a fresh-context Sonnet subagent (general-purpose), told to run the
suite itself, try to break the script live, and not take anything on faith.
Verdict: **one confirmed test-coverage gap, no shipped-code bugs.**

1. **CONFIRMED, fixed.** The original "AC4" test case only asserted that
   `expected-chromium-version.js chromium` and `... chromium-headless-shell`
   both print a non-empty version. The reviewer proved this is tautological
   in this environment: they patched the script to hardcode
   `browserName = 'chromium'` (ignoring `argv[2]` entirely — the pre-fix
   behavior) and the full suite, including this case, still passed, because
   this environment's `browsers.json` currently pins the identical
   `browserVersion` for both entries. **Fix:** replaced/extended the case to
   assert that a bogus entry name (`this-entry-does-not-exist-632`, absent
   from `browsers.json`) makes the script fail — an implementation that
   silently ignores `argv[2]` would keep resolving `"chromium"` regardless
   and succeed anyway, so this actually exercises the plumbing rather than
   its output. Re-verified TDD-style: reproduced the reviewer's exact
   hardcoded-`browserName` patch and confirmed the new assertion now fails
   against it (`expected a bogus browsers.json entry name to fail, but it
   printed [149.0.7827.55]`), then restored the real file and confirmed the
   full suite (7 cases now) passes again.

Everything else the reviewer checked came back clean, independently
verified live rather than by reading the diff: `PLAYWRIGHT_CHROMIUM_EXECUTABLE`
precedence, the `set -euo pipefail` / bodyless-`if` fallthrough interaction
across all three resolution stages, the possibly-empty-string array-append
idiom, the unmatched-glob fallthrough, the version-mismatch warning citing
the correct variant's `browsers.json` entry, a genuine no-op on a normal
dev/CI machine (falls through to `playwright install`, `docs-shots.sh`
untouched), and that `smoke-launch.js`/`playwright.docs.config.ts` are
already variant-agnostic (pass `executablePath` straight through) and
correctly need no changes. No scope creep — diff and the review both
confirm exactly the three named files, all under `e2e/scripts/`.

## What was verified beyond automated tests

- Ran `resolve-chromium_test.sh` directly, myself, multiple times: pre-fix
  (all 6/7 relevant new cases genuinely fail), post-fix (all pass), and
  after the review's follow-up fix (all 7 pass, including the corrected
  case 6/7).
- Drove the real resolver directly (`PLAYWRIGHT_CHROMIUM_EXECUTABLE=`
  `bash scripts/resolve-chromium.sh`) against this session's real
  `/opt/pw-browsers` install: resolves the headless-shell binary, prints
  the loud version-mismatch warning correctly tagged
  `(chromium-headless-shell)` (this repo's `@playwright/test` pin, 149.x,
  vs. the session's installed 1194-revision browsers, 141.x — a genuine,
  pre-existing, and already-handled version drift, unrelated to this card).
- No UI, no i18n surface, no Go code touched — `gofmt`/`go build`/`go test`/
  the CI-blocking guards are not applicable to this diff (bash + Node CLI
  tooling under `e2e/scripts/` only); confirmed no other file references
  the changed internals beyond `docs-shots.sh`'s existing stdout-path
  contract, which is unchanged.
- No real client/shop name or secret-shaped literal anywhere in the diff.

## Verdict

**Safe to merge.** One test-coverage gap found and fixed (scoped to the
fix, not a re-review of the whole diff, per the pipeline's one-review-round
default). No blocking issues.
