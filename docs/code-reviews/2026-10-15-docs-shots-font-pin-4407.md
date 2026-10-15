# Review — docs-shots: pin the reference fonts (ut-docs#4407)

**Branch:** `fix/4407-docs-shots-font-pin` · **Card:** universaltill/ut-docs#4407 (complexity:easy)
**Author:** Sonnet dev subagent (orchestrated) · **Reviewer:** Opus 5.5, fresh context (different model)

## What shipped

- **Root cause:** the app ships no webfont. `web/public/app.css`'s body stack
  (`-apple-system, …, Roboto, "Helvetica Neue", Arial, sans-serif`) resolves to
  whatever system fonts the generating machine has. universal-till#1954's shots
  came from a machine that resolved it to an Arial-metric face, so the next
  cloud-lane run re-rendered `reports`/`invoices` in Inter like every other topic.
  The card's "webfont not loaded in time" theory was wrong: there is no webfont.
- `e2e/fontconfig/fonts.conf` (new): the harness's own fontconfig — Inter
  aliases for the stack, then the system config. `playwright.docs.config.ts`
  passes it to the docs Chromium only, as `FONTCONFIG_FILE`. No `/etc/fonts`
  change needed; the e2e suite and the product are untouched.
- `e2e/tests-docs/lib.js` + `docs-shots.spec.ts`: a precondition in `capture()`
  (once per locale) probes, via CDP `CSS.getPlatformFontsForNode`, which
  platform fonts hidden body-inheriting probes really render with, and fails
  the run unless Latin is exactly Inter and Arabic-script is DejaVu Sans
  (Inter allowed for spaces). The message names the resolved fonts and the fix.
- `docs-shots-determinism.yml`: installs `fonts-inter fonts-dejavu-core`;
  `e2e/fontconfig/**` added to its paths filter.
- `e2e/README.md`: the prerequisite, why, and the accepted gaps.
- No PNG changes in the final diff: the 8 `reports`/`invoices` shots were
  regenerated in Inter on this branch, but universal-till#1965 (ut-docs#4408)
  landed first and regenerated the same topics from a cloud container. After
  rebasing, the pinned harness reproduced `main`'s PNGs byte-identically
  (120/120), which is the fix working. `manifest.json` unchanged.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **Blocker** | Inter came from a hand-placed, unpackaged `/etc/fonts/conf.d/56-prefer-inter.conf` on this container; `fonts-inter` ships no fontconfig snippet. On a clean machine (and the CI runner) Arial → Liberation Sans wins, so the new check would fail every run and the README/"Fix" text was wrong. | **Fixed:** the aliases are committed as `e2e/fontconfig/fonts.conf` and applied via `FONTCONFIG_FILE`. A first attempt using a relative `<include>` silently didn't load (fontconfig resolves it against its own dir) — caught by the check itself; aliases are now inline. |
| 2 | Minor | Arabic failure hint claimed Inter supplies spaces even when it doesn't. | Fixed (reworded). |
| 3 | Minor | `monospace` text (codes, chips) and emoji are not pinned. | Accepted gap, documented in `e2e/README.md`. |
| 4 | Minor | `querySelector` → nodeId 0 would give an unclear CDP error if a probe were missing. | Accepted: cannot happen as written (inserted just before). |
| — | Found in testing | On a cold fontconfig cache one probe query returned no fonts. | Fixed: probe waits two frames before querying. |

Checked fine by the reviewer: probe can't affect captured pixels (hidden,
absolute, removed before the `fonts.ready` + rAF waits; no relevant
MutationObservers); per-locale cache sound with `workers: 1`; CDP sequence valid.

## Verified beyond automated tests

- `make docs-shots`: 120/120 passed; exactly the 8 expected PNGs changed vs
  `main`; after the fontconfig fix a second full run was byte-identical to them.
- **Clean-machine proof:** with `/etc/fonts/conf.d/56-prefer-inter.conf` moved
  aside (restored after), `fc-match sans-serif` → DejaVu Sans, yet the harness
  passed `reports`/`invoices`/`catalog` in all 4 locales (12/12) byte-identical.
  Before the fix the same setup failed with "Latin text resolved to [Liberation Sans]".
- Deliberate fail: `PINNED_FONTS.latin = 'Nope'` → the test fails with the message.
- Visual: old `en/reports.png` is an Arial-metric face, new one is Inter like the other topics.
- 47/49 `ci.yml` build-job guards pass; `guard-deadcode-baseline` (tool built
  with Go 1.26 vs go1.27 module) and `guard-shellcheck-version` (no shellcheck
  in this container) fail on the environment — no Go or shell changed.

## Residual / deferred

- Font *version*: the check pins family names, not versions; noble's
  `fonts-inter` (4.0) matches this container. A different Inter build would pass
  the check but could shift glyphs.
- The reused Chromium (141) differs from the Playwright pin (149) — ut-docs#622.

**Verdict:** safe to merge once `docs-shots-determinism` is green on the PR
(that job is the real clean-runner test of finding #1).
