# Review — report retention cloud upload (ut-docs#574, ADR-0040 card 4, ADR-0147)

- **Date:** 2026-10-03
- **Lane:** lane:cloud-54
- **Author model:** Opus 5.5 (Dev subagent). **Reviewer:** Fable (independent subagent, scratch worktree at `cb01be2`).
- **Design:** ut-docs ADR-0147, which amends ADR-0040 §1/§2.

## What shipped

- **Uploader** `internal/cloudsync/report_archives.go`. This runs on the main till only, inside Tick's existing main-till block next to `pushSalesAggregates`.
  - It only runs in mode `cloud` or `both`, at most once every 10 minutes, and sends up to 50 rows per round, oldest first.
  - It posts to `POST /v1/stores/report-archives` with the period encoded for the wire per ADR-0147 §3: `:`→`-`, `+`→`p`, `.`→`_`.
  - Responses:

    | Response | What the till does |
    |---|---|
    | 200 | Sets `cloud_acked_at` and clears the refusal. |
    | 402 | Records the till-local refusal key `cloudsync.report_archive_refused_at` and stops the round. |
    | A content 4xx | Skips the row. |
    | Anything else | Stops the round; it retries next round. |

- **Repository** `internal/data/report_archive_cloud_repo.go`. It adds methods to list, count and ack un-acked rows, plus two new prunes: acked, and acked-and-older-than.
  - Every prune, including the existing age prune, is one `DELETE`.
  - That `DELETE` never removes a kind's newest-`created_at` row or its highest-`z_number` row (ADR-0147 §4).
- **Prune wiring** `eod_api.go`:
  - main till in mode `till`: deletes by age;
  - main till in mode `cloud`: deletes acked rows;
  - main till in mode `both`: deletes rows that are acked and old;
  - a replica deletes by age in every mode.
  - The audit entry records the mode, the predicate and whether this is the main till.
- **Mode setting**, moved to `report_retention_api.go` so that `eod_api.go` stays off the entitlement import list.
  - `till` is always accepted.
  - A change into `cloud` or `both` needs `entitlement.Allows(EffectivePlan, CapCloudBackup)`. Without it the request gets a translated 409.
  - An unknown mode gets a translated 400.
- **Settings card:**
  - The cloud options are enabled only when the entitlement allows them.
  - On the main till, the card shows how many uploads are pending and a warning when an upload was refused.
- **Status-bar chip** `GET /ui/report-archive-chip`. It shows when the refusal key is set, the mode is not `till`, and this is the main till. It is a plain link, never a modal. It is registered in the auth background-poll and shell lists and in the demo allowlist.
- **i18n:** 7 new keys in en/ar/fa/tr, and `settings.retention.mode_coming_soon` removed. The de/es packs follow in their own PRs.
- **Help:** the `reports` topic is updated in en/de/ar/fa/tr. `make docs-shots` was regenerated; only the manifest changed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | When a subscription lapses, the current `cloud`/`both` option rendered `disabled selected`. A disabled option is not submitted, so Save posted `mode=""` and got a misleading "choose where to keep reports" error. | **Fixed.** The option stays enabled when it is the saved mode. The handler gates only a *change* into cloud/both, because ADR-0147 §1 says a lapse never resets the mode. Test: `TestPostSettingsReportRetention_LapsedShopCanResaveCurrentMode`, seen failing (409) without the fix. |
| 2 | should-fix | There was no test for the invalid or empty `content_json` branch. | **Fixed.** `TestPushReportArchives_InvalidContentSkipped` covers it: the row is skipped and stays un-acked, and the next row uploads. |
| 3 | nit | The audit `entity_id` is set to the cutoff even for the acked predicate. | Accepted. The payload carries the predicate. |
| 4 | nit | Only the requesting till applies the gate. The main till's `/api/sync/settings/apply` does not re-gate. | Accepted. This conforms to ADR-0147 §1, and the replica's cache is relayed from the main till. |
| 5 | nit | After a reactivation with nothing pending, the refusal clears only on the next 2xx, which can take up to a day. | Accepted. This is what ADR-0147 §2 says. |
| 6 | nit | There are now two status-bar pollers, each every 60 s. | Accepted. The cost is small. |

The reviewer judged these correct:
- the data-integrity core: the keep clause, NULL `z_number`, rows kept per kind, and the prune/ack race;
- the wire contract against ut-cloud's `report_archives.go` and `reportarchive.ValidIdentifier` / `dateShapedPeriod`;
- fail-closed gating;
- the per-till refusal key;
- the chip wiring and i18n.

No file writes, and no cwd-relative paths.

## TDD re-verification (reviewer, in its worktree)

- With the keep clause removed from `PruneReportArchiveAcked`, `TestPruneReportArchiveAcked_KeepsNewestRowAndZChainContinues` failed (`pruned 3, want 2`). Restored, it passed.
- With `pushReportArchives` moved outside the main-till block, `TestTickReportArchives_MainTillOnly` failed (`replica uploaded 2 report archives`). Restored, it passed.
- For finding 1, the orchestrator removed the unchanged-mode exemption and the new test failed (expected 204, got 409). Restored, it passed.

## Verified beyond automated tests

See the close-out comment on ut-docs#574 for the Tester's driven run: a real till against a locally run ut-cloud, the lapse scenario, and screenshots.

## Verdict

Safe to merge.

## Deferred

- Uploading a replica's Z archive needs a till key on the cloud row. A follow-up card is being filed.
