# Review — release notice on a self-order till (ut-docs#4394)

Date: 2026-10-15 · Lane: cloud-24 · Author: Sonnet 5.5 (dev subagent) · Reviewer: Opus 5.5 (independent, fresh context)

## What shipped

- `internal/httpx/httpx.go`: the `releasenoticeversion` template func is now
  `releasenotes.NoticeVersion` directly. The `releaseNoticeVersion` wrapper
  that returned "" whenever the till-wide self-order flag was on is gone.
  That flag can't tell a customer from a signed-in manager (same mistake
  ut-docs#4324 fixed for the record dialog). Its only consumer, `base.html`'s
  status-bar chip, is operator-only and gated on `allowed "settings"`. The
  customer-facing kiosk pages (`self_order*.html`) never include base.html.
  So the gate never protected a customer. It only hid "what's new" from a
  manager doing a maintenance sign-in on a satellite kiosk (#4307) or
  browsing that till from a phone.
- Comments updated in `httpx.go`, `internal/pages/release_notes.go` and
  `base.html`.
- Tests:
  - `TestReleaseNotesChip_ManagerOnlyIncludingOnSelfOrderTill` (renamed from
    `…NeverOnKiosk`): with self-order mode on, a manager sees the chip and a
    cashier still doesn't.
  - `TestReleaseNotesChip_CustomerKioskTemplatesNeverRenderIt` checks every
    `self_order*.html` template (8 files) for the chip.
  - `TestReleaseNotesChip_NotOnRenderedSelfOrderPage` renders the anonymous
    `/self-order` with a notice pending and asserts there's no chip.
- `web/help/img/manifest.json`: the surface hash was refreshed with
  `update-docs-shots-surface-hash.sh`. No docs-shot covers self-order mode or
  the release chip (`e2e/tests-docs/docs-shots.spec.ts`), and the base.html
  edit only touches a comment, so no rendered pixel changes.

## Findings (independent review) and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | — | Key question: can a customer reach a base.html page with `allowed "settings"` true on a self-order till? The reviewer traced `allowedFunc` → `menuPredicateChecker` → `canPerform`. It is false with no session and false with no checker wired. That covers RenderError, `/login`, `/setup` and auth redirects. Kiosk handlers render partials from `self_order*.html` and use `http.Error`. `/` redirects to `/self-order`. No auto-login session carries `settings`. | No path found |
| 2 | minor | Residual case: with `UT_AUTH=off`, `canPerform` is true for everyone. | Accepted. This is dev/CI only and nothing in packaging sets it. With auth off, /settings itself is open anyway. |
| 3 | minor | Review record missing | This file |
| 4 | minor | Content conflict in `httpx.go` with open PR universal-till#1947 (#4324). That PR rewrites the `selfOrderMode` comment and the wrapper's body. | Whichever lands second resolves it: delete the wrapper, keep `InitSelfOrderMode`/`SelfOrderMode()`, and drop the "releaseNoticeVersion reads it" sentence. Once both land, `selfOrderMode` has no production template reader. Superseded by round 2 below: the atomic is retired in this PR (ut-docs#4396). |
| 5 | nit | `web/help/*/updates.md` ("Cashiers never see this note, and neither do customers at a self-order kiosk") | Still accurate, so left as is. |
| 6 | nit | The render test calls `InitI18n` without cleanup | Matches existing tests, so left as is. |

## TDD

- Dev: the renamed test failed before the fix ("a manager on a self-order
  till must still see the chip").
- Reviewer re-verified in its own worktree. Reverting `httpx.go` made the test
  fail. Restoring it made the test pass.
- Mutation check: the reviewer injected the chip into `self_order.html`. Both
  kiosk tests failed. Injecting it into `self_order_shop.html` tripped the
  scan test.

## Verified

- `go build ./...` passed.
- `go vet` and `golangci-lint` on `internal/httpx` and `internal/pages`
  reported 0 issues.
- `go test ./internal/... -count=1`: every package passed except `internal/plugins`.
  That one hit the 10-minute timeout in `TestHostSecretSet`, with the machine
  loaded by concurrent guard and review runs. A solo re-run was cut off by
  the session's background limit. It doesn't depend on `internal/httpx` or
  `internal/pages` (`go list -test -deps`), so the diff can't affect it, and
  CI's `build` job runs it.
- Every `ci.yml` script guard passes, except two that also fail on clean
  `origin/main` in this container:
  - `guard-deadcode-baseline`: desktop build skipped.
  - `guard-shellcheck-version`: wrong shellcheck version installed.
- No visual surface was looked at. The chip's look is unchanged from #3091,
  which screenshotted it at 1024×600/360, RTL and dark. Only who can see it
  changed, and the handler tests cover that.

## Round 2 — CI: deadcode baseline (retires `selfOrderMode`, ut-docs#4396)

#1947 merged while this PR was in review. On the rebased head, `desktop-shell`'s
`Deadcode baseline` step failed. It flagged `httpx.SelfOrderMode()` (added by
#1947) as unreachable, because this PR removed `releaseNoticeVersion`, the
last production reader of the till-wide `selfOrderMode` atomic. A new
baseline entry may not ride the PR that made the code dead (ut-docs#3406).
The atomic was write-only by then, and no surface may gate on it
(#4324, #4394). So this PR retires it:
- `selfOrderMode`, `InitSelfOrderMode` and `SelfOrderMode` are removed, along
  with their three production call sites (`pages/init.go`, `settings_page.go`
  ×2) and the `SnapshotStateForTests` field.
- The `InitDisplayMode` calls beside them stay.
- Tests that simulated a self-order till now set `httpx.InitDisplayMode("self_order")`.
- The two freshness tests (upsert, rederive) now assert the live display mode
  through the `backtosaleurl` func instead of the removed flag.

Author: Sonnet 5.5 (dev subagent). Reviewer: Opus 5.5 (independent, fresh
context). It traced every former write site (all still publish
`InitDisplayMode`), and found no remaining reader of the atomic anywhere
(Go, templates, JS, docs). It mutation-checked the converted tests:
removing `InitDisplayMode` from the rederive path or from the upsert handler
fails `TestRederiveSettings_PublishesDisplayMode` /
`TestSettingsUpsertDisplayMode_UpdatesLiveDisplayMode`.

Findings:
- 2 minor: the comments in `release_notes.go` and
  `release_notes_kiosk_4394_test.go` still named the removed flag. Fixed.
- 1 nit: a pronoun in `kiosk_pin_login_test.go`. Fixed.
- Info, accepted: the record-dialog and release-notes tests'
  `InitDisplayMode("self_order")` setup isn't read by those templates today.
  It stays as a regression guard against gating them on display mode.

Verified locally:
- `deadcode@v0.48.0` (built with go1.27.1, `-test=false . ./cmd/unitill-uninstall`)
  reports nothing outside the baseline. The only extras are the two
  `internal/logging` entries that appear because the desktop tag can't build
  in this container.
- `go build`, `go vet` and `golangci-lint` report 0 issues.
- `go test ./internal/httpx/ ./internal/pages/` passed.

Verdict: safe to merge.
