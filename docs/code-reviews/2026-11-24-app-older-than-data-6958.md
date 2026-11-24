# Review — app older than its data: refuse the downgrade, say so in Recovery (ut-docs#6958, slice S1)

- PR: universaltill/universal-till#2709 (branch `fix/6958-app-older-than-data-v2`), paired docs PR universaltill/ut-docs#7008.
- Built by the `:41` lane (Opus); reviewed independently by Fable 5.2 (data-integrity card), swept under
  PR-SWEEP step 0's budget-exceeded exception (#5153) after the build stopped at its card budget.

## What shipped

- Boot: `db.ErrDatabaseNewerThanApp` when the database's highest applied migration is above the binary's
  highest. Recovery classifies it (`errors.Is`) as `KindAppOlderThanData` and shows
  `recovery.app_older_than_data`: update the app; safe mode kept, Retry hidden, never "delete the data directory".
- Updater: `updates.SchemaSafe` — a pre-release/nightly till is never offered or given a stable release in-app
  (releases don't publish their schema yet). Check now shows a held-back line; apply returns 409 `newer_data`;
  `selfupdate` refuses before the lock or any download.
- Help: `web/help/en/recovery.md`, `web/help/en/updates.md`; docs: `architecture/packaging.md` (ut-docs#7008).

## Findings

| # | Severity | Where | Finding | Outcome |
|---|---|---|---|---|
| 1 | minor | `internal/updates/updates.go` `SchemaSafe` | Unstamped `dev` builds stay fail-open: a manual Update now can still install a stable over newer data. Lands on the new recovery text, no data loss. | Accepted for S1; follow-up on the card (decide with F1). |
| 2 | minor | same | "Plain release" = no `-` suffix, not validated; a dash-free non-semver stamp would pass. Nothing stamps one today. | Accepted; follow-up on the card (strict `X.Y.Z` check). |
| 3 | minor | `internal/db/db.go` | Colliding branch migration numbers at or below the binary's max still get the old "delete the data directory" advice via `verifyAppliedMigrations`. | Out of S1 scope; belongs to F4. |
| 4 | minor | `update_api.go` | Pre-release tills are frozen out of in-app updates with only the Check-now signal. | Accepted, documented trade-off; F2. |
| 5 | nit | `web/help/en/recovery.md` | "Safe mode (below)" — it is described above. | Fixed. |
| 6 | nit | `setup_update_check.go` | Wizard apply answers "already current" when held back; unreachable. | Accepted. |
| 7 | nit | `backup_import.go` | New `ErrBackupNewer` mapping is unreachable (`checkCandidateSchema` catches it first). | Accepted (defence in depth). |
| 8 | nit | `recovery/serve.go` | Safe mode reads a newer schema with older queries — same risk class as the existing migration case. | Accepted. |

## Verification

- TDD claims re-verified by the reviewer in a separate worktree: `SchemaSafe` forced true → `TestSchemaSafe`,
  `TestCheckNowHeldBackForPreReleaseBuild`, `TestApplyRefusesStableOverPreReleaseBuild` fail; the boot check
  removed → `TestOpen_DatabaseNewerThanAppIsItsOwnError` fails with the incident's own text; restored → pass.
- `go build ./...`, `go vet` (touched packages), `gofmt -l`; `go test -count=1` on `internal/db`, `updates`,
  `selfupdate`, `recovery`, `pages` — pass. Guards: i18n, i18n-meta, help-topics, data-access, no-showmodal,
  no-native-confirm, rtl-logical-css, compliance-claims, fixed-dates — pass. Full CI on the PR.
- Tester (build cycle): driven run with a ledger row above the binary's max and a fake v0.36.3 feed — new
  recovery text in en and ar at 1024×600 and 360px; held-back line on Check now.

Pre-flight: help topic updated (`recovery.md`, `updates.md`; screenshots unchanged — text-only), en.json +
en.meta.json updated (English only), reference doc updated (`architecture/packaging.md`, ut-docs#7008),
README n/a (no claim changed), follow-ups listed on ut-docs#6958 (F1–F4 + findings 1–2).

## Verdict

Approve with the one nit fixed. Deferred: findings 1–3 as follow-up lines on ut-docs#6958.
