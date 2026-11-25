# Review: keep the backup key and the TSE credential out of phone backups (ut-docs#6918)

Date: 2026-11-25 · Lane: lane:cloud-41 · Branch: `fix/6918-backup-exclude-keys-fiscal`
Complexity: easy · Built by Sonnet · Reviewed by Opus 5.5, in a fresh context

Pre-flight: Any change: review record = this file; help = `web/help/en/backups.md` updated (English only, prose, no screenshot change); en.json/migrations/catimport = n/a; shellcheck `scripts/ci/*.sh` = 0 issues; guards run = guard-android-backup-rules (+_test), ios-secrets-backup_test, guard-ci-tests-sandboxed, guard-help-topics, guard-compliance-claims, guard-help-terms, guard-help-breadcrumbs, guard-i18n; `go build ./...` OK (no Go change).

## Change

- Android (`data_extraction_rules.xml` for both cloud backup and device transfer, and `backup_rules.xml`) and iOS (`TillServer.excludeSecretsFromBackup`) now also exclude `keys/` and `fiscal/`.
  - `keys/` holds `offsite-backup.key`, the off-site backup DEK.
  - `fiscal/` holds `signing_device_credential.json` and its legacy name.
- `guard-android-backup-rules.sh` and `ios-secrets-backup_test.sh` require the two new folders. They also pin the Go paths, with comment lines stripped: `paths.Data("keys")` plus `keyFileName`, and both `RelPath` vars plus their `paths.Data(...)` uses. Moving either file out of an excluded folder fails CI.
  - New fixtures cover each removed exclude, each moved path (one variable at a time), and code that moved while a comment kept the old string.

## Why exclude both (BA/Architect)

- **Off-site key.** A copy in a phone backup is an undeclared extra holder of the key. That contradicts ADR-0199 and the "keep it only on this till" choice.
  - Recovery on a new phone already exists, with tests in `internal/offsitebackup/keyflow_test.go`: link via my. (cloud copy), enter the saved `UTK1-` key, or start a new key.
  - With the key file gone but a cloud copy present, `ensureKey` returns `errNeedsLink`, never a silent new key.
  - Residual risk, which ADR-0199 accepts: a shop with no cloud copy and no saved key loses its *older off-site* backups if the phone dies. `Older` generations are in the same file, but rotation isn't built yet, so in practice only generation 1 exists. The database itself still restores from the phone backup.
- **TSE credential.** A transferred copy leaves one live fiskaly key on two devices. Re-provisioning mints a new key and revokes the old one (ADR-0053), which is better.

## Review findings (Opus) and outcome

1. **Major, fixed.** The help named a "Settings → Fiscal" section that doesn't exist, and promised a self-service re-setup that doesn't exist either. It now says the till can't sign receipts until signing is set up again, and to contact support.
2. **Minor, fixed.** Key names in the help now match the topic ("the backup key", "receipt-signing (TSE) credential").
3. **Minor, fixed.** The Go-path pins could false-pass on comments, and the fiscal pins checked only the definitions. Comments are now stripped and the uses are pinned too.
4. **Minor, fixed.** The `credential-moved` fixture moved both vars at once. It is now split into one fixture per variable.
5. **Minor, deferred to ut-docs#7050.** A restored main till keeps `signing_device_configured=true` with no credential, and nothing prompts a re-setup. ut-docs#7050 covers it with a marker and a boot check, and the help gets the self-service step there.
6. **Nit, fixed.** Comment wrapping.

Tester (separate agent) also found SC2015 ×2 in `ios-secrets-backup_test.sh`. Fixed: `shellcheck` now reports 0 issues.

## Verification

- **Mutation run, in a temp copy:** each removed exclude and each moved Go path fails the matching guard, and the unmutated tree passes. The XML-only mutations correctly leave the iOS test unaffected, and vice versa for the Swift list.
- **Go side:** the data root is `filesDir` (Android) or `Application Support/till` (iOS), set via `mobile.start` → `paths.Init`. An empty pre-created 0700 `keys/` or `fiscal/` gives `ErrNoKey` or not-stored, and Save then Load works. This was checked with a throwaway overlay test; `go test ./internal/offsitebackup/... ./internal/fiscal/...` OK.
- **Not run here:** Gradle and Swift compiles. They run in android-ci and ios-ci on the PR.
