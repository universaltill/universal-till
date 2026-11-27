# Review — replica plugin sync: a relisted plugin keeps its rollback snapshot and is re-keyed (ut-docs#7034)

Date: 2026-11-27 · Lane: lane:cloud-41 · Branch: `fix/7034-relist-plugin-rollback`
Author (Dev + fix round): Sonnet subagents · Review: Opus 5.5 subagent (fresh context) · Tester: separate
Opus subagent · Fix-round re-check: Opus orchestrator (different model from the author).

Pre-flight: till user-visible behaviour: none (replica background sync only; no UI, no help topic change);
docs-shots n/a; `docs/reference/` n/a; ut-docs `architecture/lan-sync.md` relist paragraph updated
(ut-docs branch `docs/7034-relist-rekey`); ADRs: none changed; `README.md` n/a; new `en.json` keys: none;
migrations: none (`listing_id` is already the primary key of `plugin_install_status`); review record: this file.

## What shipped
- `internal/data` `InstallStatusRepo.DeleteForListing(listingID, pluginID)`: compare-and-delete on both ids;
  `internal/plugins` `InstallStatusStore.DeleteListing` wrapper (no-op on a blank id).
- `internal/pages/sync_admin.go` `convergePluginSet`, for a row whose plugin P still has an Active record
  under a listing L1 the main no longer lists (`relistSource`):
  - before `pluginSyncInstall`, an Active copy of the L1 record is saved under L2 (version = the installed
    one), so the ut-docs#495 rollback path finds a prior good version: a pin mismatch rolls back instead of
    uninstalling P, and a plain failure keeps L2 Active (no heartbeat "install failed");
  - after a successful install (only if L2 is then Active for P) and in the already-converged branch
    (`rekeyRelisted`: save L2 first, delete only if that succeeded), L1 is deleted by listing id;
  - never overwrites or drops for an L2 record that is Active for another plugin (`listingHeldByOther`);
  - a failed pre-adopt save skips that row's install for the tick; a cloud-hosted refusal removes the copy.
- `cloudsync_wire.go` is unchanged (the design moved the fallback to the caller, which knows P before install).

## Tests (`go test ./internal/pages -run 7034`, `./internal/data -run DeleteForListing`)
Eleven `…_7034_…` tests in `internal/pages/sync_plugin_relist_7034_test.go` (stub and real-installer):
same/new-version re-key, install failure keeps both records Active then recovers, rekeyed-then-dropped
is pruned once, real-installer pin mismatch rolls back (no `versions/1.1.0`, L1 kept), real-installer plain
failure is not "failed", plus the fix-round cases below. `TestInstallStatusRepo_DeleteForListing`.

## TDD evidence
- Dev, before the fix: L1 record still present; no pre-adopted L2 record; P uninstalled
  (`installed=("",false)`); L2 recorded `failed`.
- Tester mutation checks: disabling each of the three fix sites in turn fails at least one test; loosening the
  repo delete to `WHERE listing_id = ?` fails `TestInstallStatusRepo_DeleteForListing`. File restored byte-identical.
- Fix-round tests were written after the code (stated by the Dev agent).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | Medium | The L1 drop after a successful install trusted the row's plugin id: an L2 bundle installing as Q would delete P's only Active record. | Fixed: re-read L2, drop only when Active for P; test `InstallLeavingOtherPluginUnderL2KeepsL1`. |
| M2 | Medium | Pre-adopt / rekey could overwrite an Active L2 record of another plugin. | Fixed: `listingHeldByOther`; test `OtherPluginsL2RecordNotOverwritten`. |
| L1 | Low | Adopted version came from the L1 record, not the installed version. | Fixed. |
| L2 | Low | Cloud-hosted refusal left the pre-adopted copy behind. | Fixed; test `CloudHostedRemovesPreAdopted`. |
| L3 | Low | While a relist is failing, P has two Active records; if the main then drops P, the prune removes it twice in one tick (second is a no-op). | Accepted: harmless today. |
| L4 | Low | A failed pre-adopt save still ran the install without rollback protection. | Fixed: skip the row this tick; test `PreAdoptFailureSkipsInstall`. |
| L5 | Low | Test gaps (mismatch test: L1 kept, new version dir absent; failed rekey save). | Fixed; test `RekeySaveFailureKeepsL1`. Setting survival on the real installer is not asserted: the fake manifests declare no settings, and a same-listing #495 mismatch drops an undeclared key the same way (pre-existing, unchanged here). |
| L6 | Low | Review record missing. | This file. |

## Verification
`gofmt -l .` clean; `go build ./...`; `go vet` (pages, data, plugins); `golangci-lint run ./internal/...` 0 issues;
`go test -race ./internal/pages -run '7034|7012'`, `-race ./internal/data -run InstallStatus` pass;
`guard-data-access.sh`, `guard-fixed-dates.sh` pass. Full-package `-race` runs did not finish on the 4-CPU
cloud box; CI runs the full gate.
