# Review: a backup restore keeps this till's own cloud identity (ut-docs#5069)

- **Card**: ut-docs#5069, part of ut-docs#5058 (replace a dead main till).
- **Branch**: `fix/5069-restore-keeps-identity`.
- **Investigation**: ut-docs `reference/investigation-5058-backup-restore.md`
  §1.4 ("Cloud identity is the dead till's"), recommendation 1.

## What shipped

- `db.ApplyPendingRestore` (boot, before the DB is opened) rewrites the
  staged backup so the running till's cloud identity wins:
  - the backup's `RestoreKeepsLivePrefixes` rows are deleted
    (`secure_delete`, so the other device's token bytes are zeroed) and
    the live DB's rows are copied in. The set is
    `TillCloudIdentityPrefixes` (device id, token, enrolment/paired/terms
    markers, `till_identity.*`, unpaid check-in) plus what is only valid
    with that credential: `marketplace.store_id`,
    `marketplace.merchant_id`, `marketplace.public_key`, `entitlement.*`,
    `cloud.link_*`, and this device's support session `diagnostics.*`;
  - `RestoreDropKeys` (the "what this till last pushed" hashes) are
    deleted, so the restored data is pushed again;
  - `cloudsync.catalog.*` stays the backup's: it describes the restored
    rows (the generation each row is stamped with).
  - No live DB (or one without a settings table): the restored till has
    no cloud identity and mints/pairs its own; never the backup's.
  - A live DB that exists but cannot be read (the corrupt-DB case a
    restore cures, normally with the till's own backup): the backup's
    identity is kept as before, so the recovered till stays paired.
- A LAN join (replica identity staged) is untouched: `ApplyReplicaIdentity`
  owns that case and the main till's store link must travel.
- A staged file that cannot be rewritten (not a till database) is
  withdrawn (it is a copy of a file still in `backups/`); the live DB stays
  and boot goes on (before, it was swapped in and failed at Open →
  recovery mode). A `restore-refused` marker next to the DB makes Settings
  → Backups show "The last restore was not applied…" to a manager until
  the next restore is staged (new key `settings.backup.restore_refused`).
- Help `backups.md` (en/de/tr/ar/fa): one paragraph on both behaviours.
- `TestPushHashesAreDroppedOnRestore` (cloudsync) fails when a new
  `cloudsync.*_hash` key is added without `db.RestoreDropKeys`.
- Payment/plugin secrets: unchanged (sealed under the key file outside the
  DB; the restored ciphertext stays unreadable on another device, as
  before).

## Tests (TDD)

`internal/db/restore_identity_test.go`, a backup built as another device
(own device id, token, store, entitlement, `till_identity.cloud_id`, push
hash) restored onto:

- a paired till: its device id, token, store, merchant, paired_at and plan
  stay; the backup's data (`marker.sales`, `store.name`) and catalogue
  cursor come back; the dead till's extra identity rows are gone; the
  dead token's bytes are not in the restored file;
- an unpaired till and a data folder with no DB: no identity from the
  backup at all;
- a LAN join snapshot: store link kept (regression guard);
- an unreadable staged file: refused, live data kept, not re-staged,
  refusal recorded and cleared by the next staging;
- an unreadable live DB: restore applies, the backup's own identity kept,
  push hash dropped;
- `TestSettingsPage_ShowsRefusedRestore` (pages): the notice shows for a
  manager after a refusal, not before, not for a cashier.

Re-verified: with `backup.go` reverted to `origin/main`, the four
behaviour tests fail with the real errors (dead device id/token restored,
unreadable file applied); with the fix they pass.

## Independent review (Fable, read-only; ran build, vet, db/cloudsync/app/enroll/pages tests: all ok)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A corrupt live DB restored from the till's OWN backup would drop its identity and force a re-pair | Fixed: unreadable live DB keeps the backup's identity; test added |
| 2 | major | No help/doc text; a refused restore was only logged | Fixed: help paragraph in 5 locales, Settings → Backups notice + key; ut-docs `architecture/local-backup.md` update handed to the orchestrator (other repo) |
| 3 | minor | `restore-rejected-*.db` redundant and never pruned | Fixed: the refused staged copy is removed |
| 4 | minor | `diagnostics.*` (support session) and `marketplace.public_key` still travelled | Fixed: both kept from the live till. `auto_register_opt_in` travelling: accepted (pre-existing semantics of an unpaired till; the replace flow pairs first) |
| 5 | minor | Test gaps (unreadable live DB; cloud.link_/terms_/unpaid_checkin rows) | Fixed |
| 6 | nit | `temp_store(2)` parity; duplicated prefix-delete loop | Fixed: DSN parity, shared `deleteSettingsPrefixes` |

Verified by the reviewer, not findings: the LAN join path is unchanged; a
VACUUM INTO file is rollback-journal mode, so the in-place rewrite is
crash-safe and idempotent; `tills` roster, `sync.*`, `cloud_outbox`,
`cloudsync.catalog.*`, stock stream rotation correctly travel with the
data. Side benefit: reading the live DB first checkpoints its WAL, so the
pre-restore copy no longer loses uncheckpointed rows (only when readable).
Pre-existing, out of scope: the window between the two renames in
`ApplyPendingRestore` (a crash there boots an empty DB; the pre-restore
copy holds the data) — reported to the orchestrator for a card.

## Gate

`go build ./...`, `go vet`, full `go test ./...` green; every ci.yml guard
green except two environment-only ones locally (shellcheck 0.11 vs 0.9
pin; `guard-store-private.sh` awk multibyte under the Mac locale, green
with `LC_ALL=C`). `golangci-lint` could not run locally (built with an
older Go); CI runs it.

## Verdict

Safe to merge. UI: one notice line in Settings → Backups (text only,
existing `row-warn-icon` style); not looked at in a browser in this card —
the Backups card is screenshot-checked with #5070's changes.

## Docs-shots

`make docs-shots` cannot run on this Mac (its font pin needs the Linux
reference fonts). No captured pixel changes: the only screenshotted route
touched is `/settings` (the display topic), whose shot shows the default
section, not the Backups card, and the new notice renders only after a
refused restore; `backups.md` has no `routes:`. The manifest's
surface hash was refreshed with the generator's own
`e2e/tests-docs/write-manifest.js` (`Docs-Shots-Unchanged: true`).
