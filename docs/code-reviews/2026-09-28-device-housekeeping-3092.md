# Review — device housekeeping: daily clean-up job + retention table (ut-docs#3092)

Date: 2026-09-28 · Lane: `lane:cloud-24` · Built by Opus 5.5, reviewed by Fable (independent, read-only subagent).

## What shipped

- `internal/housekeeping` (new): `Run(dbPath, now)` applies every file sweep once;
  `Retention()` is the written retention table (every kind of stored file, its limit,
  what enforces it — incl. the log and diagnostics caps enforced elsewhere);
  `Schedule` runs it at the first check after boot, then once per 24h.
- `db.PrunePreRestore` (new): `pre-restore-*.db` copies set aside by a restore were
  **never pruned** before; now newest 3, none older than 30 days, aged by the restore
  time in the file name.
- `issuereport.PruneOlderThan` (new): unsent bug-report bundles (can hold video) had no
  cap; now dropped after 7 days. Meta-less dirs age by mtime (a mid-`Save` dir is fresh).
- `selfupdate.PruneStaleAttempts` (new): Windows `updates/attempt-*` older than 7 days;
  `updater.log` kept; no-op during installer hand-over.
- `db.DefaultBackupKeep = 14` replaces the literal in the daily backup, "Back up now"
  and housekeeping.
- `server.runHousekeeping` hooked into the existing hourly daily-backup loop; logs each
  sweep with its policy. Failures are logged and skipped; selling never waits on it.
- Manual: "Automatic clean-up" section in `web/help/{en,de,tr,fa,ar}/backups.md`
  (translated in-cycle); ut-docs `architecture/local-backup.md` updated (separate PR).

Scope split (filed as Backlog cards): low-disk floor + status warning (ut-docs#3121),
Settings → Storage with preview (ut-docs#3122), sync-history table retention with a
table allow-list guard (ut-docs#3123).

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| B1 | blocker | `Retention()`/`days()` unreachable → `guard-deadcode-baseline` red | **Fixed** — `runHousekeeping` logs each sweep's policy from `Retention()`. Remaining `logging.Stderr`/`timestampWriter.Write` entries also appear on `main` locally (desktop root skipped without GTK headers) — pre-existing, not this diff. |
| M1 | major | `os.Rename` keeps the old DB's mtime, so a till idle > 30 days that restores would lose its fresh safety copy on the first run | **Fixed TDD** — `TestPrunePreRestore_AgesByRestoreTimeInName` failed ("the copy set aside two minutes ago was removed"), passes after aging by the name's timestamp (mtime fallback). |
| m1 | minor | no clock-sanity floor: an RTC far in the future expires everything age-based in one pass | **Accepted** — only files already bounded by count or disposable (pre-restore copies, unsent reports, update downloads); snapshots are count-only. Noted for ut-docs#3121. |
| m2 | minor | uploader vs expiry race on a day-7 bundle | **Accepted, documented** in `PruneOlderThan`'s comment (one failed upload; same outcome as expiring earlier). |
| m3 | minor | DB-guard test was alias-blind and missed `internal/data/<sub>` | **Fixed** — resolves `internal/db`'s local name, forbids `.`/`_` imports, prefix-matches forbidden paths; mutation (`dbx.Open`) now fails the test. |
| m4 | minor | server test ran housekeeping against default `./data` paths | **Fixed** — test points `paths` and `issuereport.PendingDir` at a temp dir. |
| n | nit | fa "گزارش حسابرسی" ≠ audit log | **Fixed** — uses the product's own term `audit.title` ("گزارش تغییرات"). |
| n | nit | "downloaded updates" is Windows-only | Accepted — true statement for users on every platform. |
| n | nit | ADR-0040 citation | Checked — ADR-0040 is report retention (the card cites it too); correct. |

## Verified beyond unit tests

- **TDD re-verified inline** by mutation (revert → fail → restore → pass): age limit in
  `PrunePreRestore`; dropping the issue-report sweep from `Run`; hand-over check in
  `PruneStaleAttempts`; `Schedule.Due` always-true; aliased `db.Open` in the guard.
- **Driven run** of the real binary (`UT_DATA_DIR` temp dir, two 60-day-old
  `pre-restore-*.db`, one 27-day-old and one fresh unsent report): 2 min after boot the
  log showed `[Backup] daily snapshot` then `[Housekeeping] pre_restore_copies: removed 2`
  and `unsent_bug_reports: removed 1`; the fresh report and the new snapshot remained.
  Process stopped, port freed.
- Gate: `gofmt`, `go vet ./...`, `go test ./...`, `golangci-lint` (0 issues), guards
  help-drift, help-topics, compliance-claims, i18n, data-access, core-neutral,
  competitor-naming. No UI surface changed (help prose only) — nothing visual to look at.
- Not verified: a real Windows or Android device run (Windows-only attempt folders are
  covered by unit tests; tablet disk before/after belongs with ut-docs#3121).

## Verdict

Safe to merge after the fixes above.
