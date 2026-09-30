# 2026-09-30 — The main till reports its shop name on check-in (ut-docs#3246)

Branch `feat/3246-heartbeat-store-name`. Lane `lane:cloud-24`. Built by Opus 5.5 (Dev
subagent); reviewed by Fable. Paired with ut-cloud `feat/3246-till-store-name-checkin`,
which adopts the name unless a cloud rename is still unacked. The ordering is in the
ADR-0095 note (ut-docs).

## What shipped

- `internal/pages/cloudsync_wire.go`: `remoteStoreNameReport`. `DeviceExtra` adds
  `store_name` only on a till that decides for itself (`SyncPrimaryURL == ""`, the same
  gate as `directive_key`). The value goes through `config.NormalizeStoreName`. The key is
  left out when the name is unset, blank, a placeholder, invalid, or can't be read. No
  timestamp is sent, because till clocks are never compared (a Pi without a real-time
  clock can boot with a wrong time).
- Help: `web/help/{en,de,ar,fa,tr}/display.md` step 9 gains one sentence: a name saved on
  the main till shows on the cloud manage page after the till's next check-in.
- No new locale keys, so no language-pack follow-ups.

## Docs-shots manifest (hand-updated, deliberately)

`make docs-shots` can't run in the cloud container (no browser).
- `surface_sha256` was refreshed with `scripts/ci/update-docs-shots-surface-hash.sh`.
- The `display` topic hashes (en/fa/ar/tr) were set to the sha256 of each edited file,
  which is the guard's own formula.
- The Go change is a heartbeat hook, and the help edits are prose. No rendered pixel
  changes.
- The reviewer checked each hash, and `guard-docs-shots.sh` passes. The commit carries
  `Docs-Shots-Unchanged: true`.

## Review findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The manifest hand-edit needs the trailer and a stated reason. | Done (above and on the commit). |
| 2 | minor | The Arabic sentence attached "next" to the device, not the connection. | Fixed. |
| 3 | nit | Farsi used "تماس" for check-in. | Fixed; it now says "همگام‌سازی", matching the preceding sentence. |
| 4 | nit | The new sentence named my. differently from the sentence before it. | Fixed; every locale now says "cloud manage page". |

Checked with no finding:
- the gate is the same one `directive_key` uses;
- placeholder and normalize rules match the cloud's;
- a read error omits the key rather than sending a blank.

## Verification

- TDD: `TestBuildCloudHooks_MainTillReportsStoreName` failed first
  (`store_name = <nil>`).
- The reviewer mutated the code in a scratch worktree, and each change was caught:
  - dropping the assignment fails the main-till test;
  - hoisting it out of the gate fails `TestBuildCloudHooks_AdditionalTillOmitsStoreName`.
- `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint`: see the PR.
- Guards: data-access, i18n, help-topics, help-drift, docs-shots, compliance-claims and
  competitor-naming all pass.
- Not visually checked: no UI surface changed, only help prose.

## Verdict

Safe to merge.
