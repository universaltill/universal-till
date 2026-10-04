# Code review — no-sale drawer opens, slice A (ut-docs#2558)

Date: 2026-10-03 · Lane: lane:cloud-24 · Author: Opus 5.5 (Dev subagent) · Reviewer: Fable (fresh context)

## What shipped

Before this change the till could not open the cash drawer without a sale. The only drawer kick was the cash-tender receipt. So the ADR-0111 aggregate's `no_sale_count` and `no_sale_opens` were always 0.

This change adds:
- **Migration 064 `no_sale_events`.** An append-only table plus a `no_sale_events_archive` reset twin. Its `local_date` is computed the same way as for sales. It has no foreign keys, because the drawer is already open when the row is written.
- **`internal/data/no_sale_repo.go`.** Inserts an event and counts events per (day, till) and per (day, till, actor). It uses the same till key as `tillKeyExpr`.
- **`internal/print/drawer.go`.**
  - `OpenDrawer` sends only the `ESC p` pulse for the configured pin, with no paper and no cut.
  - Sentinel errors cover the cases where no printer is set or the printer type is system/lp.
- **`POST /api/pos/no-sale`.**
  - Gated by `checkOrElevate(..., "cash_adjustment", pin)`, the same gate as the shift skim.
  - It kicks the drawer, then records the event and an audit row (`InsertAuditElevated` when a PIN was used).
  - If the kick fails, nothing is recorded: 409 for no printer or an unsupported printer, 503 for a transport failure.
  - It uses the `{data, error}` envelope, is demo-denied, and is not auth-exempt.
- **Cloud aggregate.** It now fills `no_sale_count` and per-cashier `no_sale_opens`. A (day, till) or a cashier with only no-sale opens still produces a rollup or bucket.
- **Housekeeping.** Reset/restore and sync-admin classification are updated, and so is `docs/data-model.md`.

The UI is not part of this slice. The button, EOD/Z line, manual and i18n are on #3560.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The record step ran on the request context after the drawer had physically opened. A client disconnect could therefore leave the drawer open with no row. | **Fixed.** `context.WithoutCancel` is now used after the kick, following the `engine_config.go` precedent. |
| 2 | minor | Opens on an additional till never reach the main till's rollup, but the comments implied they were journaled. | **Comments corrected.** The gap is filed as #3562 and noted in ut-docs ADR-0111. |
| 3 | nit | The `utf8.ValidString` check could never fire, and it was labelled `reason_too_long`. | **Fixed** (removed). |
| 4 | nit | An empty body returned 400. | **Fixed.** An empty body now counts as an empty request; new test `TestNoSale_EmptyBodyIsAnEmptyRequest`. |
| 5 | nit | The `actorID == ""` fail-closed branch is unreachable, because `auth.UserID` returns `system`. | Accepted. It is harmless and mirrors the skim. |
| 6 | nit | The till key would diverge if a client ever sent `registerId` on a sale. | Accepted. No client sends it today. |

The reviewer also checked these and found them correct:
- the authorization semantics and how actor and approver are recorded;
- the drawer bytes;
- the UNION placeholder order;
- deterministic cashier merge, so the upload hash stays stable;
- reset round-trip and migration safety;
- no raw SQL outside `internal/data`/`internal/db`, no file writes, no card data.

## TDD re-verified

- The reviewer reverted the aggregate wiring, and `TestPushSalesAggregates_NoSaleOpens` failed (`no_sale_count = 0, want 3`).
- The reviewer made the handler ignore the kick error, and all 3 subcases of `TestNoSale_KickFailureRecordsNothing` failed.
- I reverted the empty-body fix myself, and `TestNoSale_EmptyBodyIsAnEmptyRequest` failed.
- With the fixes restored, all of these tests pass.

## Gate (after the review fixes)

- `gofmt -l .`: empty. `go build ./...`, `go vet ./...` and `go test ./...` all pass. `golangci-lint`: 0 issues.
- These guards pass: data-access, card-data-schema, i18n, kiosk-engine, core-neutral, no-showmodal, help-topics, compliance-claims, competitor-naming.
- `guard-deadcode-baseline` can't run cleanly in the cloud container, because GTK headers are missing and `cmd/unitill-desktop` is skipped. CI runs it.

## Verdict

**Safe to merge.** Not driven on real hardware: there is no UI yet, and the drawer kick is verified byte-for-byte against a device-file transport.

## Deferred

- #3560: UI, EOD/Z line, manual, and possibly a dedicated permission.
- #3562: journal secondary tills' no-sale opens to the main till.
