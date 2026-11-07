# Review: `till.drawer.no_sale` event after a recorded No sale (ut-docs#5758)

- Date: 2026-11-07. Lane: lane:cloud-41. Card: universaltill/ut-docs#5758 (complexity:medium).
- Built by: Sonnet (Dev subagent). Reviewed by: Opus 5.5 (independent subagent, separate worktree). Tester: separate subagent.
- Contract: ut-docs `reference/contracts/till-drawer-no-sale.md` v1.0.0 (ut-docs PR for #5758).

## What shipped
- `internal/plugins/ipc.go`: the `till.drawer.no_sale` constant, a payload type with six string fields (`no_sale_id`, `register_id`, `till_id`, `actor_id`, `approver_id`, `created_at`, with no free-text reason), `PublishTillDrawerNoSale`, and explicit NonBlocking registration.
- `internal/plugins/event_dataclass.go`: data classes `{sales, staff}`.
- `internal/plugins/manifest_abi3.go`: `"till"` added to `coreEventRoots`. A plugin id that is `till` or starts with `till.` is refused, and `event_publish` refuses `till.*`, so no plugin can forge the event.
- `internal/pages/no_sale_api.go`: dispatch only on the local path, after the transaction commits. It is gated by `HasSubscribers`, so with no subscriber nothing is built and no audit row is written. It uses `context.WithoutCancel` and recovers from panics. The response is always the same 200.
- `internal/pages/sync_no_sales.go`: a comment only. The LAN-journaled replica path never publishes.
- Docs: `docs/plugin_guidelines.md` (data-class list) and `docs/arch/plugin-integration-roadmap.md` (core-seam row).

## Findings (review round 1)

| # | Sev | Where | Finding | Status |
|---|---|---|---|---|
| 1 | Medium | `internal/pages/no_sale_api.go` `sendTillDrawerNoSale` | Publish used the request ctx. If the client hangs up after the kick (kiosk navigates, tablet drops Wi-Fi), `CheckPermission`'s DB read fails with `context canceled` -> subscriber audited `denied` and the event is lost although the row committed and the drawer opened. Confirmed: new test fails without the fix ("no till.drawer.no_sale event received"). | **Fixed**: `ctx = context.WithoutCancel(ctx)` inside `sendTillDrawerNoSale` (after the HasSubscribers gate, so the 0-alloc path is unchanged) + `TestTillDrawerNoSale_CancelledRequestContextStillDelivers`. |
| 2 | Medium (cross-repo) | ut-cloud `pkg/manifest/validate.go:52` | ut-cloud's `coreEventRoots` is a documented mirror of the till's ("mirrors universal-till/.../manifest_abi3.go") but has no `till`, and no mirror test guards it. The marketplace would accept/sign a plugin id `till.*` (or a schedule event `till.*`) that the till then refuses at install. | **Deferred -> ut-docs#5768**: add `"till": true` in ut-cloud, ideally plus a `manifest-contract-guard` mirror test for `coreEventRoots`. Not fixable in this worktree. |
| 3 | Low (doc accuracy) | ut-docs `reference/contracts/till-drawer-no-sale.md` "Namespace" | "No plugin id may start with `till`" is wrong as written: the check is on the first dot-segment, so `tilly-shop` is fine; only `till` and `till.*` are refused. | **Fixed** in the ut-docs PR: "No plugin id may be `till` or start with `till.`". |
| 4 | Low (doc accuracy) | contract "Publish point" / "Delivery semantics" | "after the HTTP response is written" — `writeJSON` only buffers; net/http delivers the 200 when the handler returns, i.e. after Publish (one `CheckPermission` read + 2 audit writes per publish). The drawer kick and the commit are already done, so offline-first holds; only the UI confirmation waits a few ms (more under SQLite write contention). Same as the `customer.erased` precedent (`data_api.go:436`). | **Accepted, contract wording fixed** (precedent, cost small, NonBlocking never waits on a plugin). Suggest contract wording: "after the response is written (sent when the handler returns); a client disconnect does not cancel the publish." |
| 5 | Nit (pre-flight) | `docs/arch/plugin-integration-roadmap.md` | Core-seams table lists every core event seam (`customer.erased`, ...) but not the new one. | **Fixed**: one row added. |
| 6 | Nit | ut-docs ADR-0153 amendment "Today: ..." list | Lists event->class sets as of 2026-10-15; the new set is not there. Informational list, not a contradiction (the ADR requires each core event to have a set, which this does). | Accepted; optional one-line addition in the ut-docs PR. |
| 7 | Info | `sync_no_sales_test.go` `TestSyncNoSalesAPI_PublishesNoTillDrawerEvent` | Regression guard only (there is no line to revert on the sync path); it passes trivially today but will catch a future publish there. | Accepted. |

Checked and clean: no raw SQL outside `internal/data` (test SQL only); no country/vendor/plugin ids in core
(the payload/test name `com.test.signer` is test data); no user-facing strings; signing/verification untouched;
no file writes / cwd paths (recurring bugs n/a); fixed dates `2026-01-01` are >60 days back; no double publish
(one call, after the `err` return; the RecordFailure test proves the 500 path); only one insert path publishes
(`InsertNoSaleEvent` via handler; `ApplyJournaledNoSaleEvent` never); `till` root breaks no in-repo plugin id,
schedule, fixture or e2e (grepped `plugins/`, `internal`, `e2e`, testdata; ut-cloud seed data too).
Contract vs code: event key, NonBlocking, six field names/types, `""` semantics, `till_id` always `""` in v1,
data classes {sales, staff}, permission `events:receive`, not-fired cases — all match.

## Verification run (worktree)
- `go build ./...` OK; `go vet ./internal/plugins/ ./internal/pages/` OK; `gofmt -l internal/` empty;
  `golangci-lint run ./internal/pages/ ./internal/plugins/` 0 issues.
- `go test ./internal/pages -run 'NoSale|TillDrawer'` ok; `go test ./internal/plugins -run 'TillDrawer|DataClass|EventRoot|PluginID|EventPublish'` ok.
- Full `go test ./internal/pages/` ok (327s) with fixes; `./internal/logging` ok.
- Full `./internal/plugins/` exceeds go's default 10m in this sandbox (timed out in unrelated `wasm_tcp`/`TestHostSecretSet`,
  same as the Tester's run); re-run with `-timeout 50m` (with fixes): **ok (741s)**.
- Guards rc=0: data-access, core-neutral, i18n, fixed-dates, mail-addresses, no-showmodal, kiosk-engine, compliance-claims.
- TDD re-verification (revert -> fail -> restore, `git diff` clean after each):
  1. drop `"till": true` -> `TestPluginID_TillRootRefused` FAILS (`validatePluginID("till") = nil`, `checkPluginEventName = outside the plugin's own namespace`).
  2. bypass `HasSubscribers` -> `TestTillDrawerNoSale_ZeroSubscriberAllocatesNothing` FAILS (30 allocs/op) and `TestNoSale_NoSubscriberWritesNoEventPublishedAudit` FAILS.
  3. remove the dispatch call -> `TestNoSale_PublishesOneEventAfterCommit_ExactPayload` FAILS (no event received).
  4. (my fix) remove `WithoutCancel` -> new cancelled-ctx test FAILS; restored -> passes.


## Verification
- Tester: 9 mutations (HasSubscribers guard, post-commit dispatch, `till` root, data class, reason in payload, Blocking mode, dispatch from sync, and more). Each one turns a test red. The only survivor was removing the explicit NonBlocking line, which matches the bus default. The Tester added `TestNoSale_PublishSeesCommittedRow`. It covers publish-inside-tx, which before had only deadlocked to the timeout.
- `go test ./...` green (plugins 763 s, pages 338 s, the other 100 packages pass). Targeted `-race` runs clean. vet and golangci-lint: 0 issues. Guards: core-neutral, data-access, fixed-dates, i18n, kiosk-engine, mail-addresses, card-data-schema and netaccess all pass.
- Driven run: a real till was built and started. Two No sales returned 200, wrote their rows and audit rows, and wrote no `event_published` row with no subscriber. A delivery run with a real signed plugin subscriber was not done. Delivery is covered in-process by httptest tests.
- Reviewer re-ran 4 TDD claims (revert → red → restore → green).

Pre-flight: plugin event/core root -> `docs/plugin_guidelines.md` updated; ut-docs contract + plugin-architecture.md updated
(wording fixes applied); ut-cloud `coreEventRoots` mirror deferred -> ut-docs#5768; `reference/plugin-host-functions.md`
n/a (no host function); taxonomy/manifest field n/a; `web/help/` n/a (no shop-owner-visible change); en.json n/a; README n/a;
roadmap updated (#5); this record.

## Verdict
Approve with fixes (all ported). Deferred: ut-cloud `coreEventRoots` mirror, `shift` root spoofing, and the mirror contract test are tracked on ut-docs#5768.
