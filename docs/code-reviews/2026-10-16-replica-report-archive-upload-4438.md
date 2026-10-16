# Code review — additional tills upload their own Z-report archives (ut-docs#4438)

- **Date**: 2026-10-16
- **Card**: universaltill/ut-docs#4438 (ADR-0147 amendment 2; follows ut-docs#3565, whose cloud `till_id` contract is on ut-cloud `main`)
- **Author**: Opus 5.5 (lane:cloud-41). **Reviewer**: Sonnet 5.5, fresh context. Fable was the mapped reviewer but returned HTTP 429 (out of usage credits), so the review fell back to Sonnet per MODEL-ROUTING.md.

## What shipped

- `internal/cloudsync`: `pushReportArchives` now runs on every till, not only inside Tick's main-till-only block.
  - The main till sends no `till_id` (`omitempty`), so its wire contract is unchanged.
  - An additional till sends its `sync.till_id`.
  - A joined till with no roster id yet, or with an id that fails the identifier regexp, sends nothing that round. It never uploads with an empty `till_id`, which would file its chain as the main till's.
  - The throttle, batch, and the ack / 402 / rejection / retry outcomes are unchanged.
  - The refusal state is the existing per-till key.
- `internal/pages`: `pruneReportArchive` uses the mode table on every till. A replica now prunes acked rows in `cloud` and acked + 10-year rows in `both`, and the keep-newest/highest-Z rule is unchanged. The retention card's pending count, the refusal warning and the status chip now show on a replica too.
- Help: `web/help/{en,de,tr,ar,fa}/reports.md` now say that each till uploads its own reports. The structure is unchanged, and `guard-help-drift` is clean.
- Comments in `internal/data/report_archive_cloud_repo.go` are updated.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Stale comments in `report_archive_cloud_repo.go` still said "only the main till uploads" and "mode cloud on the main till". | Fixed. |
| 2 | minor | A replica with no `sync.till_id` yet would fall through `discovery.ReportedTillID` to its LAN id. Rows closed in that window would go up under a second till_id, which splits the chain in the cloud. | Fixed. The replica now reads `sync.till_id` only and waits until it is set. New test: `TestPushReportArchives_ReplicaWithoutRosterIDWaits` (seen failing first with "posts = 2, un-acked = 0"). |
| 3 | minor | Replica promoted to main: its un-acked rows go up under `""`. If the old main's chain holds the same (kind, period), cloud idempotency returns 2xx and the row is acked while the cloud keeps the old content. | Accepted. Periods are second-resolution close instants, so this is very unlikely, and the same behaviour already exists for a restored main till. Documented in the ADR "Role changes" bullet. |
| 4 | nit | A doc comment line was wider than the surrounding wrap width. | Fixed (rewrapped). |
| 5 | nit | `reportArchiveOnMainTill` is now used only for the prune audit's `main_till` field. | Its comment already says so. No change. |

## Verified

- TDD: the reviewer reverted the four non-test files in an isolated worktree. Every new test failed for the claimed reason: the replica uploaded 0, the invalid id posted 2, there was no till_id on the wire, the replica pruned by age only, and the card and chip were missing on the replica. With the files restored, all of them pass.
- Cloud contract was checked against ut-cloud `internal/httpapi/handlers/report_archives.go`:
  - `till_id` is optional and trimmed;
  - it is checked with `ValidIdentifier`, the same alphabet as the till's regexp;
  - idempotency is keyed on `(store, till_id, kind, period)`, so two tills closing in the same second get distinct rows.
- Prune safety: `cloud_acked_at` is set only by this till's own 2xx, and `report_archive` is per-till and not synced, so a replica never prunes a row the cloud does not hold.
- Offline-first: the checkout path is not touched. Upload state shows only as a status chip and card text, with no modal.
- Gate: gofmt, `go build ./...`, `go vet ./...`, golangci-lint (0 issues), `go test ./...`, and every guard in `ci.yml`.
- UI: the replica's card and chip reuse the existing partials and strings, with no new keys. These were checked through rendered HTML in httptest only. I took no screenshots and did not run the app in a browser, because the visible surfaces have no layout change.

## Verdict

Safe to merge. Finding 3 is the only item left open, and it is accepted and documented.
