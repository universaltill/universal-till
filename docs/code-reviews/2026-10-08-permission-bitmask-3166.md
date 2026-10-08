# Review: in-memory permission bitmask (ut-docs#3166)

Date: 2026-10-08 · Lane: lane:cloud-24 · Author: Opus 5.5 · Reviewer: Fable (independent subagent)

## What shipped

- `AuthRepo.HasPermission` (`internal/data/auth_repo.go`) now answers from
  an in-memory bitmask (ADR-0128 §6). Each `permission_actions` row gets a
  bit, and each role's granted rows become a `[]uint64`. The check is
  `mask[word]&bit != 0`. An unknown role or action is denied, and so is
  `granted = 0`.
- The bitmask is keyed on `sync_admin_version.generation`. Migration 023's
  triggers bump it on every `roles`, `permission_actions` and
  `role_permissions` write, which covers local edits, directive apply and
  bundle apply.
  - The generation is read **before** the rebuild scan, the same ordering
    as `SyncAdminRepo.ensureCached`. A write that commits mid-scan costs
    one extra rebuild. It never leaves a stale grant cached.
  - The comparison is `!=`, so a lower generation also rebuilds.
- **No usable counter** (missing row or table) → the bitmask is dropped
  and the row lookup answers, the same rule as `adminGeneration`.
- **Plugin install/remove:** plugins write none of these tables today
  (nothing under `internal/plugins` touches them). ADR-0128 §6's "rebuilt on
  plugin install/remove" is therefore covered by the generation key, and no
  separate hook was added.
- The long-lived `auth.Service` owns one `AuthRepo`, so the cache lives
  as long as the process.
- No migration, no UI, no i18n, no manual change: behaviour is identical.

## Tests (TDD: written first, failed to compile on the missing bitmask)

- `TestAuthRepo_HasPermission_BitmaskMatchesRowLookup` checks every
  role × action, plus an unknown role and an unknown action, against the
  old row lookup. The catalog is padded past 64 actions so masks span two
  words, and one seeded grant is set to `granted = 0`.
- `TestAuthRepo_HasPermission_EditTakesEffectWithoutRestart` revokes a
  grant through `SetRolePermission` on the same repo, then adds a new
  action and grant. Both take effect on the next check, and the cached
  generation moves.
- `TestAuthRepo_HasPermission_NoGenerationRowFallsBack` deletes the
  counter row and confirms a revoke is still seen.
- Mutation-checked by hand. Each of these mutations made a test fail:
  - always reading word 0;
  - never rebuilding on a generation change;
  - ignoring `granted`.

## Findings

| # | sev | finding | outcome |
|---|---|---|---|
| 1 | blocker (from the gate, not the review) | `TestCan` (`internal/auth`) uses a fixture with no `sync_admin_version` table. The gen read errored, so `Can` returned an error. | Fixed: any gen-read error falls back to the row lookup, which still fails closed on a real DB error. |
| 2 | test | `TestResolved_DbErrorFallsThrough` renames `role_permissions` to prove a failed permission read fails closed. A rename fires no trigger, so the cached mask still granted. | Fixed in the test: it now bumps the generation, so the rebuild hits the missing table and fails closed, as before. |
| 3 | nit | The fallback path left a stale `r.perm` that could be served if the counter came back with the same generation. | Fixed: the fallback clears it. |
| 4 | low | Per-check cost is still one single-row SELECT. That is the floor for an immediately consistent cache, because a time-based throttle would honour a revoked grant. The real saving is amortising the generation read per request. | Accepted. No measurable per-check win is claimed. Per-request amortisation is follow-up card ut-docs#3935. |
| 5 | nit | The mutex is held across the rebuild scan with the caller's ctx. | Accepted: two tiny queries, the same as the `ensureCached` precedent. |
| 6 | nit | ADR wording on plugin install/remove. | Noted in the code comment and in this record. |

The reviewer checked every write path to the three tables (all of them
fire migration 023's triggers, and nothing disables them). It also checked
uncommitted-transaction and WAL snapshot ordering, generation decreases
(restores apply only at boot), and fail-closed errors. It found no
stale-grant window.

## Verified

`gofmt`, `go build ./...` and `go vet` are clean. `go test ./...` passes,
and these packages also pass under `-race`: `internal/data` (permission
tests), `internal/auth` and `internal/pages/common`. The CI guards
`data-access`, `netaccess`, `core-neutral`, `pipefail-grep-q`,
`card-data-schema`, `kiosk-engine`, `i18n`, `migration-version-collision`
and `page-http-error` all pass. `golangci-lint` can't run in this container
(its binary is built with Go 1.25, the module needs 1.27.1), so CI's `lint`
check covers it. Only the `unused` linter is enabled, and nothing new is
unused.

**Verdict:** safe to merge.
