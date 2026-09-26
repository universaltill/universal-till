# Review: diagnostics.*, cloudsync.*, install.* become per-till settings (ut-docs#2950)

- **Date:** 2026-09-26
- **Lane:** lane:cloud-24 (built on Opus 5.5, reviewed by Fable in a fresh context)
- **Branch:** `fix/2950-per-till-settings-scope`

## What shipped

ut-docs#2791 had classified some settings families as shop-wide only
because the admin bundle carried them. This change reviews each key the
card listed and decides its scope.

Moved to `data.PerTillSettingPrefixes`: the main till's admin bundle no
longer sends them, and an additional till no longer applies them.

- `diagnostics.*`: this till's ADR-0092 support session.
  `internal/diagnostics` documents these rows as till-local. When synced,
  the main till's session switched a replica's session on or off.
- `cloudsync.*`: the hashes of what THIS till last pushed to the cloud.
  Only the main till reads them, but every change moved the admin
  fingerprint, causing the same re-pull churn as #2792.
- `install.*`: this machine's one-time OS provisioning marker.

Kept shop-wide, with the reasons written at `ShopWideSettingPrefixes`:
`till.name`, `menu.restored_keys`, `lan_discovery.till_id`,
`setup.restore_prompt_status` and `fiscal.tse_provisioning_state`.
`lan_discovery.till_id` on a replica is the main till's id, and pre-#2722
re-discovery depends on it.

`db.ApplyReplicaIdentity` now also drops the join snapshot's
`diagnostics.*` and `cloudsync.*` rows. The snapshot is a whole-DB copy,
and admin pulls no longer overwrite those keys, so without this a till
joining during a support session would keep the main till's session.
`install.*` is kept at join, because a cleared marker would re-run
provisioning over the owner's window mode.

The docs change, the per-till list in `ut-docs/architecture/lan-sync.md`,
is on ut-docs branch `docs/2950-per-till-settings-scope`.

## Tests (TDD)

- `TestAdminDumpApplyRoundTrip_TillLocalStateNeverSyncs` covers both
  directions: the dump omits the three families, a pull never overwrites
  the replica's values, a pre-fix main till's bundle is refused, and the
  five kept keys still sync.
- `TestSettingScope_Classification` pins every decision.
- `TestApplyReplicaIdentityClearsInheritedTillLocalState` checks that a
  join drops `diagnostics.*` and `cloudsync.*` and keeps `install.*` and
  ordinary keys.
- Each test was run red with the production change reverted and green
  with it restored. The reviewer independently re-ran this for the first
  two. The author re-ran it for the third, after fixing the test's own
  seed (a `store.name` UNIQUE clash had made its first red meaningless).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The join snapshot still carries the main till's `diagnostics.*`/`cloudsync.*`, and admin pulls no longer overwrite them, so a till joining during a support session keeps that session. | Fixed in this branch (`ApplyReplicaIdentity` + test). |
| 2 | minor, pre-existing | The TSE provisioning retry tick runs on every till, not just the main till. | Out of scope: filed as ut-docs#2969. |
| 3 | nit | The comment lists `setup.`/`fiscal.` keys that the family prefixes match anyway. | Accepted; the classification test pins them. |

The reviewer challenged each decision against every reader and writer
of each key and found no stale comments.

## Gate

`gofmt -l` is clean. `go build ./...` and `go test ./...` pass.
`golangci-lint run ./...` reports 0 issues. CI guards pass except two
that were not run meaningfully here:

- `guard-deadcode-baseline` fails identically on `main` in this
  container, because it has no GTK headers to build
  `cmd/unitill-desktop`. Real CI analyses that root.
- `guard-shellcheck-version` needs shellcheck, which is not installed
  here. No shell files changed.

There is no user-visible change, so no help-topic update.

## Verdict

Safe to merge.
