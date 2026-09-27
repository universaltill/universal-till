# Review: EOD business day, report retention and auto-update schedule write through to the main till (ut-docs#2997)

- **Date:** 2026-09-27
- **Branch:** `fix/2997-settings-write-through-eod-update`
- **Files:** `internal/pages/eod_api.go`, `internal/pages/update_api.go`, `internal/pages/settings_sync_proxy.go` (comment), `internal/pages/settings_write_through_eod_update_test.go` (new), `internal/pages/update_api_test.go`, `web/help/{en,de,tr,fa,ar}/multitill.md`, `web/help/img/manifest.json`
- **Author model:** Opus 5.5 (card is `complexity:medium`). **Reviewer:** Fable, fresh context, read-only on the branch.

## What shipped

On an additional till, three Settings handlers still wrote shop-wide keys locally, and the next admin pull from the main till silently reverted them. Each one now goes through `saveShopSettings` with the #2791 semantics: one batch, no local write on failure, and an audit row only after a successful save. All five `// settings-write:allow` annotations are gone.

1. **`POST /api/settings/eod`** sends `reports.business_day_start` (shop-wide) and its four per-till siblings (`reports.eod_enabled`, `eod_time`, `eod_article_print_mode`, `eod_article_print_cap`) in one call. The per-till keys are written locally only after the main till accepted the shop-wide one. Store errors used to be ignored (`_ =`); a local store error now answers 500 `settings.error.save_failed`, and a write-through failure answers through `respondSettingsSyncError` (403/409/502).
2. **`POST /api/settings/report-retention`** sends `store.report_retention_mode` through the main till.
3. **`POST /api/settings/update-schedule`** sends `update.auto_enabled`/`update.auto_time` through the main till. "On" is always sent as the shop-wide default `""`, the value a main till already stored for itself (#2726). "Off" is sent as `"false"`. A replica used to store an explicit `"true"` locally, which the next pull reverted. Nothing changes meaning: `autoUpdateSchedule` reads `""` as on for a main till, and `followDecision`/`followCanInstall` only test for `"false"`.
4. **Help:** step 13 of `multitill` now lists the business-day start, report retention and the automatic-update schedule, in all five languages, using each locale's UI terms.

No template or locale key changed; the existing `settings.error.save_failed` key is reused.

## TDD

The six new replica tests in `settings_write_through_eod_update_test.go` were run against the pre-fix handlers first and all failed for the claimed reason:
- the three "on main" tests failed with `main till calls = 0, want 1`;
- the three "unreachable" tests failed with `= 204 "", want 502`.

After the fix they pass. The main-till control test (`…EODUpdateOnMainTillStayLocal`) passes before and after. The reviewer independently confirmed, by reading `origin/main`, that every new replica test fails there.

## Findings

| # | Severity | Finding | Disposition |
|---|---|---|---|
| F1 | blocker | `TestPostSettingsUpdateSchedule_OnIsDefaultOnMainTill`'s replica stanza still expected the old local `"true"` and failed with 502. | **Fixed.** The stanza now asserts the new contract: a replica with no main till gets 502 and nothing is written. The positive replica path is covered by `TestSettingsWriteThrough_UpdateScheduleOnMain`. |
| F2 | should-fix | The `keyReportsBusinessDayStart` doc comment still said "per-till". | **Fixed.** It now says shop-wide and cites #2997. |
| F3 | should-fix / accept | On a replica, "Update automatically" reads OFF right after a successful "on", because `autoUpdateSchedule` shows `""` as off there. Before this change the box showed on until the next pull reverted the value. | **Accepted.** Behaviour is correct on both tills. The replica box is the subject of open card ut-docs#2949 (replace it with "follows the main till"). A note was added there that this is now visible straight away. |
| F4 | nit | Because `business_day_start` is always sent, a replica whose main till is down can no longer change its own per-till EOD keys. | **Accepted.** This matches `saveShopSettings`' documented semantics and the print-card precedent. It is an admin screen, and checkout is unaffected. |
| F5 | nit | Permission mismatch: the local gate is `eod_report`/`plugin_management`, while the main till checks `settings`. | **Accepted.** Every shipped role (admin/manager/super_admin) holds all three, and cashier holds none (`001_init.sql`). Only a custom role could be refused on a replica, which fails closed. This is the same as the #2979 handlers. |
| F6 | nit | On the elevated-retry path, a write-through refusal is plain text with a non-2xx status, so htmx shows the generic alert instead of the specific message. | **Accepted.** This is the same as `print_api.go`/`invoice_page.go` today. The specific-message work is ut-docs#2982. |
| F7 | nit | A comment line in `settings_sync_proxy.go` was too long. | **Fixed.** Rewrapped. |

## Verified

- `gofmt -l .` is clean, `go build ./...` and `go vet` pass, and `golangci-lint run ./...` reports 0 issues.
- The full `go test ./...` is green after the fixes.
- Every `ci.yml` guard passes locally except the following, which are environmental:
  - `guard-shellcheck-version`: no shellcheck binary in this container.
  - `guard-deadcode-baseline`: GTK headers are missing, so `cmd/unitill-desktop` is skipped and `internal/logging` Stderr is reported. This is unrelated to this diff; real CI analyses all roots.
- `make docs-shots` ran: 124 passed. No screen changed, so the PNG re-renders were discarded as environment noise and only `manifest.json` was committed. The comment-only follow-up refreshed the surface hash with `update-docs-shots-surface-hash.sh`.
- No UI surface changed, so there was no visual check. The UX gate does not apply.

**Verdict:** safe to merge.

## Deferred

- ut-docs#2949: replica "Update automatically" box (see F3).
- ut-docs#2982: specific refusal messages instead of the generic banner (see F6).
