# Review: Restore from a file — Settings → Backups and the end of setup (ut-docs#5070)

- **Card**: ut-docs#5070, part of ut-docs#5058 (replace a dead main till).
- **Branch**: `fix/5070-restore-from-file` (on top of #5069's branch).
- **Investigation**: ut-docs `reference/investigation-5058-backup-restore.md`
  §1.3 and recommendation 2.

## What shipped

- `POST /api/backup/upload` (manager: `deny()` → `data_management`; a
  cashier gets 403 on the server, and the form is inside
  `{{ if .isManager }}`). The multipart body is streamed (no
  `ParseMultipartForm` spool) into `db.ImportBackupFile`, never into the
  system temp dir.
- `db.ImportBackupFile` (new `internal/db/backup_import.go`), on a staged
  `backups/upload-*.part` file:
  1. size cap (`backupUploadCap`: 4 GiB, and at most a third of the space
     above the low-disk floor, since a restore copies the file again and
     takes a safety snapshot; no room → 507 with its own message);
  2. SQLite header; 3. `PRAGMA quick_check` + till tables present;
  4. schema version not newer than this build's newest migration;
  5. `db.Open` on the upload's own copy — the exact migrations and ledger
     checks the next boot runs, so a file that passes opens after restore;
  5b. schema allow-list (review finding 1): every trigger and view must
     be byte-identical to one a freshly migrated database has, no virtual
     tables, no tables this build lacks (except the backup's photo table);
     the reference schema is built once per process in a throwaway file
     in `backups/`;
  6. same shop: a backup naming another `marketplace.store_id` than the
     paired till's is refused (either side empty → accepted);
  7. the copy's cloud identity is secure-deleted (another device's token
     is never stored again; #5069 also keeps the live identity at apply);
  8. renamed to `unitill-pos-<UTC>-file.db` (name reserved with O_EXCL,
     so two uploads in one second never collide): listed and restorable
     like any backup. Every refusal removes the staged file. A backup from
     before the schema reset gets its own message
     (`settings.backup.upload.error.too_old`).
- At boot (#5069's `keepLiveCloudIdentity`), a staged file whose triggers
  on `settings` are not this build's is refused before the rewrite, so a
  hand-dropped crafted file cannot act on it either.
- The answer shows what is in the file — shop name, number of sales, the
  newest sale with "Sales made after that are not in this backup" — the
  warning that a restore replaces everything (plus a line when this till
  already has sales), and **Restore this backup**, which is the existing
  step-up (manager PIN) restore of that file. Nothing changes before that.
- Every restore (list or file) checks the file exists, then takes a safety snapshot of the current
  data first (`SnapshotWithAssets`); no snapshot → no restore
  (`settings.backup.safety_failed`). Pruning runs after staging, so the
  backup being restored is never pruned. The `restore_staged` audit names
  the safety backup.
- The daily backup no longer counts an uploaded file as "today's backup"
  (`db.IsUploadedBackupName`).
- Boot sweeps `upload-*.part` left by a crash (older than 24 h).
- End of setup: the welcome page (`/import?welcome=1`, where the wizard
  lands) gets a line "Replacing a till that broke? Restore its backup file
  instead of starting again…" and **Restore a backup** →
  `/settings#settings-backup-upload`. #3709 removed restore from the wizard
  because the owner wanted import out of the wizard; this keeps it out and
  puts the pointer on the post-setup page #3709 created. Manager only.
- Demo mode denies the new route.
- Help `backups.md` (en/de/tr/ar/fa): new section "Restoring from a file".
- New keys: `settings.backup.safety_failed`, `settings.backup.upload.*`
  (title, help, label, action, checking, ready, shop, sales, newest_sale,
  no_sales, replaces, live_sales, restore, error.not_backup, error.newer,
  error.other_shop, error.too_large, error.no_space, error.no_file,
  error.failed), `import.welcome.restore.body`,
  `import.welcome.restore.action` — en/ar/fa/tr translated here.

## Tests (TDD)

- `internal/db/backup_import_test.go`: a valid backup from another device
  of the same shop is kept, listed, restorable, summarised (shop, 1 sale,
  newest sale) and carries no token bytes; refusals: too large, not a
  database, another program's SQLite, damaged (truncated), newer schema,
  another shop — each leaves nothing behind; no shop identity on either
  side is accepted; the orphan sweep.
- `internal/pages/backup_upload_test.go`: cashier 403 with nothing kept;
  manager gets the summary, the restore form for exactly that file, an
  audit row, and no staged restore; not-a-backup 400, other shop 409,
  too large 413 — each an HTML fragment htmx swaps, nothing kept; a
  restore takes a safety backup holding the current data, named in the
  audit.
- Trigger attack: a file with a trigger on settings, a changed real
  trigger, an extra view or an extra table is refused at upload; the same
  trigger file restored from the list is refused at boot with the live
  token intact. Both defences were proven by removing them (the tests
  fail).
- `internal/server`: an uploaded file does not stop the daily backup.
- `internal/pages`: the welcome page offers the restore line; plain
  `/import` does not.
- The db code was drafted before its tests; the tests were then checked
  against mutations: removing the shop check fails the other-shop case,
  removing the identity strip fails the token assertion. The server,
  welcome and handler tests were written first and failed first.

## Looked at (Tester/UX)

Throwaway till (temp `UT_DATA_DIR`, port 8093, killed after), Playwright
screenshots read one by one:

- Settings → Backups → Restore from a file: empty form, the not-a-backup
  error, the checked summary, the PIN dialog after **Restore this backup**;
  and the welcome page's restore line.
- 1024×600 in en, fa, ar, tr, light and dark (`theme=dark`); 1280×800 en;
  360×740 en light and fa.
- Found and fixed while looking: the summary first landed in the muted
  `#restore-msg` (grey, low contrast) → its own `#backup-upload-msg`; the
  error line was muted → normal text; the saved file name broke across
  the RTL sentence → `<bdi dir="ltr">` outside the translated string.
- No horizontal overflow at 360px (measured). Not looked at: German (the
  `de` strings live in the language pack, which does not have the new keys
  until its PR lands); real touch hardware and the Android/iOS file
  pickers (not available in this session).

## Independent review (Fable, read-only; ran build, vet, db/server/app/pages tests and the i18n, data-access, help and RTL guards: all ok)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A crafted backup's triggers defeat the identity strip at upload and the keep-live rewrite at boot (reproduced with sqlite3) | Fixed: schema allow-list after Open; settings-trigger check at boot; tests for both |
| 2 | minor | Two uploads in the same second overwrite each other | Fixed: O_EXCL name reservation |
| 3 | minor | Cap left no room for the safety snapshot | Fixed: a third of the free space above the floor |
| 4 | minor | A pre-reset backup shown as "damaged" | Fixed: own message `error.too_old` |
| 5 | minor | Copying the live DB without its WAL loses the last sales silently | Fixed: help says to use a backups-folder file or copy -wal/-shm too (5 locales) |
| 6 | minor | Uploaded files count toward the 14 kept backups | Accepted: they are backups of the shop; documented here |
| 7 | nit | Safety snapshot taken before the file is known to exist | Fixed: stat first |
| 8 | nit | Cashier test relied on the environment's UT_AUTH; too-large text blamed free space | Fixed both |
| 9 | nit | Placeholders in this record | Filled in |

Verified by the reviewer: step-up before any destructive change, the
cashier refusal, demo denial, no traversal via the multipart filename,
every refusal path cleans up, html escaping of the untrusted shop name,
the cap math (no underflow), htmx swapping the 4xx fragments,
translations and help structure.

## Docs-shots

Not run: `make docs-shots` needs the Linux reference fonts. No captured
pixel changes: the `/settings` shot shows the default section, not the
Backups card; `/import?welcome=1` is not a screenshotted route; the CSS
adds selectors for the new elements only. Surface hash refreshed with
`write-manifest.js` (`Docs-Shots-Unchanged: true`).

## Gate

`go build ./...`, `go vet ./internal/...`, full `go test ./...` green;
every ci.yml guard green except the two environment-only ones (local
shellcheck 0.11 vs the 0.9 pin; `guard-store-private.sh` awk under the Mac
locale, green with `LC_ALL=C`).

## Verdict

Safe to merge after #5069. Deferred: real touch hardware and the
Android/iOS file pickers; the German screens (pack PR).
