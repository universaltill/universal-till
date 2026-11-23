# Review: first-run "Start selling" did nothing (ut-docs#6929)

Date: 2026-11-23. Microsoft Store certification failure 10.1.2.10.
Author model: Sonnet (Dev). Reviewer: Sonnet-lane Reviewer (not Dev, not
Tester), inline, fresh read of the diff against `origin/main`. Tester: separate
agent (one defect, fixed by Dev in round 2, re-verified here).
Verdict: **approve** (2 small findings fixed by the Reviewer).

Pre-flight: till user-visible behaviour: no `web/help/` topic claims `/setup`
(grep: n/a, no topic, no `?` link needed; `make docs-shots` not run, wizard is
not a docs-shot surface); `reference/` n/a; README n/a; new `en.json` keys
(`auth.setup.pin_rule`, `setup.finish_busy`, `setup.busy_hint`) have
`en.meta.json` entries, English only (ADR-0160), changed `setup.error.save_failed`
text likewise; CHANGELOG updated; plan/price, platform claim, plugin surfaces n/a.

## What shipped

Root cause: the admin PIN step used native `pattern`/`minlength` on inputs in a
hidden wizard step, so a PIN that was not 4 to 8 digits made Chromium/WebView2
block the step-7 submit silently (no bubble on a hidden control). The wizard now
validates in JS (`leave6()`, `submit()`), states the rule, jumps to the failing
step with an alert, shows a busy state, keeps Next / Start selling on screen
(sticky nav, 800px compact query), and the nine bare `setup failed` 500s are
replaced by the wizard banner on step 7 (`failSetup`, logged without PIN/hash/token).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Should | Stray untracked, un-ignored `.auth-data-dir` at the worktree root (an earlier version of the hand-off wrote it there; it holds a temp path). Would have been committed by `git add -A`. | Fixed: deleted. The live mechanism writes `e2e/.auth-data-dir`, which is gitignored (`git check-ignore` confirms) and removed by the script's EXIT trap. |
| 2 | Nit | `x-init` watcher used `this.errStep` / `this.srvErrStep`; in an Alpine `x-init` expression `this` is not the data scope (the same trap Dev found in round 2), so the "retire only on leaving the owning step" comparison was always true and worked by accident (cleared on any step change). | Fixed: bare `errStep` / `srvErrStep`. login.spec.ts re-run, 37 passed. |
| 3 | Info | Save-failure spec drives a SQLite trigger via the `sqlite3` CLI and swallows its errors; on a runner without `sqlite3` the spec fails (banner never appears) rather than skips. CI images have it; the spec skips cleanly when `e2e/.auth-data-dir` is absent. | Accepted. |
| 4 | Info | Create-session failure after the PIN is stored: retry answers `303 /login`, post-session steps (base plugins, gift voucher seed) skipped. Dev documented it; rare path. | Accepted, noted for a follow-up if wanted. |

Checked and clean:
- **PIN stays server-authoritative.** Client check is advisory; `ValidatePINFormat`
  and the mismatch check still run in `POST /api/setup`; the PIN is never echoed
  back into the re-rendered form and the error messages are not PIN-derived. The
  only log line is `setup wizard: <step>: <err>` with the step name and the
  storage error, never the PIN, hash or token. The new `data-e-*` attributes
  carry translated messages only.
- **No new native constraint** on any wizard input (static Go test); step 4
  keeps `aria-required` + JS, as the template's own note requires.
- **CLAUDE.md rules:** no inline script / CSP-safe (data attributes), existing
  tokens only, logical CSS properties (`inset-block-end`, `min-block-size`,
  `border-block-start`; `.login-logo` keeps `height` beside `block-size` as the
  base rule does; `guard-rtl-logical-css` passes), English-only `en.json`,
  touch targets 48px, no network dependency added, no real shop names in test
  data (`test-6929-shop` is a placeholder).
- **RTL / long locales:** nav row is flex with logical padding; alerts use
  `overflow-wrap: anywhere`; not visually verified in ar/de/tr (Tester did not
  look either): left to the locale-render audit in CI.
- **Dead code:** none found; every state variable (`errField`, `srvErrField`,
  `busy`, ...) is read.

## TDD claims re-verified (inline, revert, run, restore in one command)

- Restored `pattern="[0-9]{4,8}" minlength="4"` on the PIN inputs:
  `TestSetupWizardPINStepStatesRuleAndNeverUsesNativeValidationOnWizardInputs` FAILS.
- Replaced `failSetup("store.name", err)` with the old bare `http.Error`:
  `TestSetupWizardSaveFailureRendersWizardBannerOnStep7ForEveryPersistPath/store.name` FAILS.
- Restored both files from backup (byte-identical, `cmp`), tests pass again.
- Tester's defect (banner off-screen, unfocused): fix is the bare `srvErrStep`
  plus rAF wait in `x-init`; the e2e test asserts `toBeInViewport({ratio:1})`
  and `toBeFocused()` at 1280x720 and passes (and passes after finding 2's edit).

## Extra verification

- `go test -count=1 -run Setup ./internal/pages` ok; full `go test -count=1 ./internal/pages` ok (374 s).
- `npx playwright test --project=auth tests/login.spec.ts`: 37 passed, none skipped.
- Tester's own full runs (go ./..., full Playwright, webkit, guards) stand; the 4 failing
  e2e specs (settings-tablet de/fa, record-dialog-lock-status import/taxcodes) fail identically on the base commit.

## Deferred / not covered

- Windows installer pass with the **signed** release artefact (AC7) and the
  Store resubmission (AC8) happen after release (new version, Publish installer
  workflow, Partner Center URL): owner/DevOps.
- Real touch + OSK open on step 6 at 1024x600, ar/fa/de/tr screenshots, screen
  reader pass: not evidenced; sticky offset under the OSK is CSS-only.
- Pre-existing `setup country arrows mirror Left/Right under RTL` flake (untouched).
- Step 4 Next stays `disabled` (UX A5 follow-up) and the Store-channel "Update now"
  banner / "v0.1-draft" terms label stay separate cards (BA non-goals).
