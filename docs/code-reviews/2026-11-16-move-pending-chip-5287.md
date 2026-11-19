# Code review: pending till move status-bar chip (ut-docs#5287 item 1)

- Repos: universal-till WIP snapshot 1cd09d03 (one commit on origin/main 18157109); ut-docs `docs/5287-move-pending-chip` (1 file).
- Reviewer: independent subagent, Opus 5.5 (the code was written by Sonnet). It ran in an isolated detached worktree.
- Date: 2026-11-16

## What shipped
- `GET /ui/move-pending-chip` in `internal/pages/move_approval.go`: an amber status-bar chip, "Till move waiting for a manager", on every page (the sale screen included) for every role. It reads memory only (`enroll.PendingMoves`) and makes no network call. It is empty on a joined till, and empty once every pending move is past its cloud expiry: the new `openPendingMove` ignores the 24 h housekeeping grace that `PendingMoves` keeps. The markup carries no shop name, store id or deadline for anyone. Only a viewer with the settings grant gets an `href` to `/settings/cloud/move/{id}` (the earliest-expiring open move). For anyone else it is an `<a>` with no href, which is plain text. The move id is not passed to the template for those viewers.
- `web/ui/partials/move_pending_chip.html` (new); a mount in `base.html` after `#sb-cloud-auth-mount` (`load, every 60s`, `hx-preserve`); `app.css`: a pill using logical properties, `:empty` collapse, the phone sale-screen `:has()` list and the drawer margin reset.
- `status.move_pending` and `status.move_pending_hint` in `en.json`, with `en.meta.json` metadata (`max_length` 40 on the chip label).
- Poll bookkeeping: `backgroundPollPaths` and `shellGetPaths` (`internal/auth/middleware.go`), and the demo allow-list (`demo_mode.go`).
- Help: `web/help/en/claim.md` steps 2–3; `claim` docs-shots regenerated (en/ar/fa/tr PNG and `manifest.json`).
- Tests: `internal/pages/move_pending_chip_5287_test.go` (6 tests) and `e2e/tests/move-pending-chip-phone-5287.spec.ts` (phone/kiosk layout; it also runs in the webkit-phone job).
- ut-docs: `reference/design-5127-till-approval-for-moves.md` §5 gets a "Status chip" paragraph, and test 13 notes the chip.

## Findings
| # | Severity | Where | Finding | Status |
|---|---|---|---|---|
| R1 | Low (test realism) | `move_pending_chip_5287_test.go` `TestMovePendingChip_ManagerLinksEarliestMove` | The test re-sent the fixture's `mvID` with a 48 h expiry. enroll ignores a changed record for a known move ("keeping the one first shown", logged as a WARN), so the "later" move actually kept the fixture's 24 h expiry. The test still passed for the right reason (2 h < 24 h), but it did not exercise what its setup claimed. | **Fixed.** It now uses a fresh `laterID` (the WARN is gone) and asserts the body does not contain `laterID`. Re-verified: reversing `openPendingMove`'s loop (pick the latest move) makes it fail; the code was restored and `git diff HEAD` is clean. |
| R2 | Info (process) | review brief | `go test ./internal/pages/ -run 5287` matches no test (none of the names contain "5287"): `ok … [no tests to run]`. A TDD re-check run that way passes vacuously. | Noted; the re-verification below used `-run 'MovePendingChip\|OpenPendingMove'`. |
| R3 | Info | `hx-preserve` mount | After a session change without a full page load (cashier to manager), the chip keeps the previous role's form until the next 60 s poll. A cashier never gets a link because of this, only a manager briefly gets plain text. This is the same as `#sb-cloud-auth-mount`. | Accepted (precedent; harmless direction). |
| R4 | Info | leak hunt | Checked: no shop name, target store, move id or expiry in the cashier markup or attributes (the title is a fixed string). The `href` id is a lowercase UUID that `enroll` validated with `uuid.Parse`, and html/template URL-escapes it anyway. The route needs a session (it is not auth-exempt). Self-order customer pages (`self_order.html`, `self_order_shop.html`, `order_tracking.html`) are standalone documents without `base.html`, so the chip cannot reach a kiosk customer. Board-only sessions get plain text. On a nil or empty list, `openPendingMove` returns false and the body is empty. At exactly `ExpiresAt` the chip is hidden (`now.Before`). The hint "review it in Settings" is accurate: the named banner with **Review** shows on every settings page. | No defect. |

## Verification (run by reviewer, worktree at 1cd09d03 plus the R1 edit)
- TDD re-verified: `canManage := true` in the chip handler makes `TestMovePendingChip_CashierOnSaleScreenAnonymous` fail, because a cashier gets `href="/settings/cloud/move/…"`. After restoring (`git diff --quiet HEAD -- internal/pages/move_approval.go`), all 6 tests pass. The R1 loop-reversal mutation was handled the same way.
- `go build ./...`, `go vet ./internal/pages/ ./internal/auth/`: clean. `go test ./internal/pages/... ./internal/auth/...` (with the R1 edit): all ok (pages 399 s, auth 6 s).
- `golangci-lint run ./internal/pages/... ./internal/auth/...`: 0 issues. `gofmt -l internal/pages/`: clean.
- Guards: `guard-fixed-dates`, `guard-docs-shots`, `guard-i18n`, `guard-rtl-logical-css`: all rc=0 after the R1 edit. The surface hash excludes `_test.go`, so no re-shoot is needed.
- ut-docs diff read: accurate. It matches the handler (cloud expiry, not the grace) and the test-13 wording.
- Tester's driven run (tester.md): the real till binary with `fake-move-cloud.mjs`, `UT_AUTH=on`, a cashier and a manager. Screenshots at 1280x800, 1024x600 kiosk (`UT_KIOSK=1`), 390x844 and 360x780 (emulated touch), and ar RTL at 390 and 1280. In every one the chip is inside `footer.statusbar`, does not intersect `#basket`, stays inside the viewport and causes no horizontal overflow. The page source holds no shop name, store id or expiry for either role. Cashier: no href, and a typed move URL gives 403. Manager: the click lands on the move page. With the fake cloud killed, the chip stays and a cashier completed a full cash sale. An empty poll body removes the chip within 61 s, and the phone status row hides again. Mutation checks: 6 mutations, all caught (Go and Playwright). Limits: dark theme and a live German label not checked (Playwright covers a 66-char German string); touch emulated only; live server-side removal not drivable (an unpaid till does not check in outside start-up; covered by the Go tests).

Pre-flight: till user-visible behaviour → `web/help/en/claim.md` updated + `claim` docs-shots regenerated, `reference/` updated (ut-docs design-5127 §5 and test 13), README n/a (does not mention moves); new `en.json` keys → `en.meta.json` updated (lang packs via Weblate, ADR-0160); plan/price n/a; platform claim n/a; ADR n/a (follows the ADR-0116 D6 chip pattern, no new mechanism); plugin/manifest n/a; review record → this file; follow-ups → ut-docs#6381 (item 2), offline expired-move Approve (below).

## Verdict
**APPROVE.** The change is small, follows the cloud-auth chip precedent closely and is cashier-safe by construction: the move id is never handed to the template for a viewer without the settings grant. It is offline-safe (memory only) and does not reach self-order customer pages. The one finding (R1) was a test-realism issue and is fixed here.

## Deferred
- Item 2 of ut-docs#5287 (rollup re-send) is split to ut-docs#6381 and goes back to the owner.
- Architect observation: while offline, the move page (`/settings/cloud/move/{id}`) still offers **Approve** for a move past its cloud expiry but inside the till's 24 h housekeeping grace. The chip already hides at the cloud expiry, so the two now disagree in that window. Card this if the owner cares (small: apply the same `now.Before(ExpiresAt)` test on the page).
