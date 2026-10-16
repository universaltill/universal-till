# Review — LAN TLS key rotation when a data directory moves to new hardware (ut-docs#4450)

**Date:** 2026-10-16 · **Lane:** lane:cloud-54 · **Author:** Opus 5.5 · **Reviewer:** Fable (independent subagent, separate worktree)

## What shipped

ADR-0114 §7's third rotation trigger ("on restore to new hardware").

- `internal/lantls/hostid*.go`: each load reads one host id per platform.
  - Linux: the devicetree board serial (a Pi), else `/etc/machine-id`, then `/var/lib/dbus/machine-id`.
  - Windows: `MachineGuid` (64-bit registry view).
  - macOS: `kern.uuid`.
  - Android, iOS and anything else: none.
- `cleanHostID` drops empty, all-zero and `uninitialized` values.
- `rotation.json` gains `host_id` = `<source>:<HMAC-SHA256>`. The raw id is never stored.
- `rotation.run`:
  - A different `host_id` (any source) creates the next key at that load, exactly as the 4-year step does.
  - A pending next key was copied too, so it is replaced and never switched to on that load (`moved`).
  - An unusable next key, or a failed replacement, keeps the old `host_id`, so a later load retries.
  - An unknown id or a first record never rotates.
  - Reading the id never fails a load.
- `ci.yml`: the cross-vet step now covers `internal/lantls` for `GOOS=windows` and `GOOS=darwin`.
- ut-docs `architecture/lan-sync.md`: the "Not yet" bullet becomes the shipped trigger, with its gaps.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | A move across id sources (Pi `dt-serial` → PC `machine-id`) was only re-recorded: it never rotated, and could switch to a copied Ready next key. | **Fixed.** Any different tag is a move. New test `SourceChangeIsAMoveAndNeverSwitchesToTheCopiedKey`; fails with the old rule. |
| 2 | low | A machine-id regenerated at every boot (volatile `/etc`) restarts the rotation each start, so it never completes. The current key keeps serving. | **Accepted, documented** (hostid.go, lan-sync.md). Rare for a till. |
| 3 | low | A Pi serial is short, so its tag can be brute-forced. The HMAC key is public. | **Comment softened** to "not stored in clear". The serial is world-readable on the same box anyway. |
| 4 | low | `hostid_darwin.go` wasn't vetted in CI. | **Fixed:** `GOOS=darwin go vet ./internal/lantls/`. |
| 5 | low | A corrupt `rotation.json` on the new machine masks the move (first-record path). | Accepted. Inherent to the storage. |
| 6 | low | The `writeNextKey` seam exists for one test. | Accepted. It defaults to `writeKey`. |

The reviewer confirmed:
- `host_id` survives every state write (`finishSwitch`, `clearNext`, `ConfirmNext`, `rotateNow`).
- A crash between the key write and the state save self-heals.
- The stale next certificate is re-issued by `ensureLeaf`.
- The build constraints are right (`android` ⊂ linux, `ios` ⊂ darwin → `hostid_other.go`).

## Verified beyond unit tests

- **TDD:** the tests were written first and failed to compile, then passed.
- **Mutations** (author and reviewer):
  - dropping the `moved` no-switch guard fails `FailedReplacementNeverSwitchesToTheCopiedKey`;
  - not recording the host fails `DifferentHardware…` and `CopiedPendingKey…`;
  - recording on `broken` fails `UnusableNextKey…`;
  - the old per-source rule fails the new source-change test.
- **Driven run** of the real binary (`UT_DATA_DIR` temp, `:18471`):
  1. The first start wrote `host_id: machine-id:<hmac>`, and the raw machine-id was absent from the file.
  2. Setting `host_id` to another machine's tag and restarting logged `this data directory now runs on different hardware: created the next key`. `next-key.pem`, `next-cert.pem` and `next_pin` appeared, and `key.pem` was unchanged.
  3. A third start kept the same next key.
- **Gate:**
  - `gofmt` clean, `go build ./...`, `go test ./...`, `go test -race` on lantls/server/fleetlink;
  - `golangci-lint` 0 issues;
  - every `scripts/ci/*.sh` in the build job passed, except `guard-shellcheck-version` (no shellcheck binary in this container; no shell file changed);
  - cross vet for windows, darwin and android.

**Verdict:** safe to merge.
