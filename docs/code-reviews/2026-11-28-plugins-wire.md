# Review: plugins-wire batch (2026-11-28)

Combined review of the P1 "plugins-wire" batch, all on ABI 2, nothing published.
Reviewers: two independent Fable subagents (core/cloud; plugins/docs), findings
triaged and fixed by the Reviewer lane (fixes by an Opus subagent, TDD,
mutation-checked). Nothing merged by this review.

## What shipped (PRs)
- universal-till #2797 (keep_years>0 => synchronous=FULL), #2798 (tips `payout:suggest` seam), #2800 (shop.context.v2 country + business_date), #2801 (sell.overlay basket seam, HMAC pick route, migration 124).
- ut-cloud #1203 (`tips` root), #1204 (records_forward in CanonicalManifest).
- ut-docs #7233 #7234 #7235 #7244 #7245 #7249.
- ut-plugin-page-appointments #3 #4; ut-plugin-page-service-logbook #1.

## Findings
| # | PR | Sev | Finding | Result |
|---|---|---|---|---|
| 1 | till #2801 | important | `ScanRefLine` returned `&s.basket` after unlocking: data race (reproduced with -race) | fixed 3fbff5b6c, test `TestScanRefLine_ReturnsPrivateBasketCopy_7206` |
| 2 | till #2801 | important | held-sale restore copied ref/plugin unvalidated; payload crosses LAN tills (HMAC and charset bypass) | fixed fa700dd84, `TestRestoreHeld_DropsInvalidPluginRef_7206` |
| 3 | till #2801 | important | nothing pinned the ref off the replica->primary `SaleDetail` wire | fixed 8e914bc63, `TestSaleDetailJSON_OmitsPluginRef_7206` (mutation: fails) |
| 4 | till #2797 | important | `KeepYears` wiring in wasm_db open path untested | fixed 4a8656f45, `TestDBOpenPathAppliesRetentionDurability_7230` (mutation: fails) |
| 5 | till #2798 | important | `tips.suggest.ask` documented non-reserved but not exempt from sale-path slot | fixed 1383594d2 |
| 6 | till #2798 | important | Tips button without help topic (rule #324) | fixed 5ca40b247 (screenshots not regenerated: `make docs-shots` pending, see below) |
| 7 | till #2798 | nit | unbounded Unmapped label / amount | fixed 7af47e2a6 |
| 8 | till #2800 | nit | midnight flake in view test | fixed cca6275be |
| 9 | till #2801 | nit | pick signatures replayable until restart | documented in ADR-0121 amendment (ut-docs #7235, 534f7af7) |
| 10 | appt #3 | important | optimistic state check compared to a fresh re-read (verified: cancelled -> checked_in); second completion overwrote sale_ref | fixed e129d5d (guarded write) |
| 11 | appt #3 | important | double-booking: check + insert as two host calls | fixed 53b130f (single guarded INSERT) |
| 12 | appt #3 | nit | receipts picker dropped newest on busy day; stale texts | fixed 200c1d7, 712f03f |
| 13 | appt #4 | nit | MaxBytes unpinned; inactive/over-cap resources dangling `r` | fixed d2cfc90 |
| 14 | logbook #1 | important | same sale line linkable twice | fixed 859f095 (migration 0002, unique index) |
| 15 | logbook #1 | important | `records_role` per entry exceeds 64-call cap (59 of 90 editable) | fixed 0dfced2 |
| 16 | logbook #1 | important | orphan sweep could delete lines of an entry mid-write | fixed 0f1cd22 (age guard) |
| 17 | logbook #1 | nit | README said ABI 3; apply fail-open on role error; TrimReplica vs truncated status | fixed b8eb1cf, 9150624 |
| 18 | docs | nit | ADR-0149 note presented static keep_years 10 as closed; wrong ADR-0221 citations; replica snapshot had no expiry; changelog label; non-existent `currency` field | fixed (7245 ba5eb0bd, 7249 128866be, 7235 534f7af7, 7233 a9f36033, 7244 e7d0aca7) |

## Accepted / deferred (-> cards, orchestrator to file)
- **Migration 124 collides with open universal-till #2765** (delivery orders, 124-126): whoever merges second renumbers and re-pins `shipped_migrations_test.go`.
- appointments: cashier role lacks the `sales` view (receipt picker fails for cashier logins): design card.
- appointments: calendar-open purge scans all named rows (perf on Pi/Android); keep_years 1 vs the 90-day default in appointment-v1 §1 (legal #6596); no-show correction overlap read; receipts view `newest_first` arg if >12k receipts/day.
- logbook: tip "already counted" check still check-then-write.
- ut-cloud #1204 does not mirror the till rule "records.received hook needs records:sync".
- Plugins can read `pragma_*` table functions (only `PRAGMA` statements and `pragma_database_list` blocked): decide separately.
- ut-docs #7235/#7249 both append to the ADR-0221 notes list: merger keeps both bullets. appointments #3/#4 conflict on CLAUDE.md layout list (keep #3 list + `src/snapshot`).
- Merge coupling: ut-cloud #1203 must merge with till #2798; ut-docs #7234 before #2797.
- Run `make docs-shots` (or update the surface hash) once after the last till merge (Tester item).

## Checked and clean
HMAC pick route (32-byte crypto/rand key, `hmac.Equal`, covers plugin+sku+qty+ref, active + `view:sales` re-check, catalogue price only, variants refused, demo-mode denied; mutation: removing the re-check fails the test); ref charset/length; migration 124 additive with archive twin, checksum pinned; sync level never lowered, `raiseSync` under the store semaphore; `AskPermitted` permission check (mutation caught); cloud-link never-list; `DefaultWasmABI`/`MaxSupportedWasmABI` stay 2 in all branches, no `wasm_abi` bump, no docs requiring ABI 3; no `PUBLISH_ENABLED` change, no tags/releases; SQL constant + bound in both plugins; int64 exact; records allow-list/`from_till_id`/replica refusal; no secrets or real shop names; no file writes without MkdirAll, no cwd-relative paths; commits authored by the developer with Claude trailer.

## Extra verification
-race on pos service; mutation checks listed above; appointments `scripts/verify.sh` and logbook gate steps re-run after fixes: pass. Real-host scripts (`realhost.sh`, `realhost-test.sh`) not re-run after the fixes (need the integrated till): Tester/orchestrator item, esp. logbook migration 0002. No browser/device drive.

Pre-flight: help topic updated (#2798 reports.md); `en.json` keys have `en.meta.json` entries (Tester fix); docs-shots deferred -> run `make docs-shots` after last till merge; host-function/manifest mirrors (till + ut-cloud) updated by #1203/#1204; ADR-0121 amendment + ADR-0221/0149 notes updated, index guard ok; review record = this file; follow-ups listed above for cards.

## Verdict
Approve to merge after the above fixes, in the order in the Tester notes, with the renumber/conflict items handled by the merger. No blocker remained.
