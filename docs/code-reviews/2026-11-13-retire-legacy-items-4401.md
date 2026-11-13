# Code review: retire the legacy item-photo symlink and start-time copy (ut-docs#4401)

Date: 2026-11-13
Repos: universal-till, branch `retire/4401-legacy-items-symlink-migration`
(commit 42e234e on `origin/main` 79b1f19, plus the reviewer's uncommitted
fixes below); ut-docs, branch `docs/4401-retire-items-symlink`
(`architecture/packaging.md`).
Author: Opus 5 build (Dev; fell back from Sonnet). Tester: separate agent.
Reviewer: Fable 5.2, independent — did not write the code.

## What shipped

Premise, groomed by the owner and not re-opened here: **no supported install
predates 2026-07-29**, the day item photo and receipt logo uploads moved to
the stable data dir (`paths.Data("public","assets",…)`).

- `packaging/scripts/postinstall.sh`: the ITEMS block is gone. It replaced
  the package-owned `/opt/unitill/web/public/assets/items` with a symlink
  onto `/var/lib/unitill/items`. `mkdir -p` now provisions `/var/lib/unitill`
  only (not `…/items`). `chown -R pos:pos /var/lib/unitill` stays.
- `internal/paths/paths.go`: `migrateLegacyUploadedAssets` (item photos and
  `receipt-logo.png`, copy-if-absent on every start) and its only helper
  `copyFileBestEffort` are deleted; `MigrateLegacyData` keeps the database
  and plugin migrations.
- `internal/paths/paths_test.go`: the three tests of the deleted code are
  removed.
- `packaging/postinstall_items_test.go`:
  `TestPostinstallItemsSymlinkIsKeptAndExplained` is replaced by the
  retirement guard `TestPostinstallLegacyItemsSymlinkRetired`.
- `internal/db/backup_assets.go`: one comment no longer points at the
  deleted function.
- ut-docs `architecture/packaging.md`: the postinstall bullet describes the
  new behaviour.

## What I checked

- **Other readers of the legacy tree.** None left that matter. Uploads
  (`internal/pages/catalog`), replica asset sync (`sync_assets.go`
  `assetScope.root`), backup (`backupAssetScopes`), the uninstaller's photo
  backup and the receipt logo (`receiptLogoPath`, read by `print_api.go` and
  `receipt_designer.go`) all resolve through `paths.Data`. The only code that
  still opens cwd-relative `web/public` is the `/public/` file server's
  second tier (`static_page.go` `newPublicFallbackFS`: data dir, then release
  tree, then embedded). It is read-only and needs no symlink: demo thumbs
  come from the embedded tier on a fresh install.
- **Receipt logo.** The legacy `web/public/assets/logo/receipt-logo.png` was
  never behind the symlink; only the start-time copy moved it. Every build
  since 2026-07-29 ran that copy on every start, and the designer has written
  to the data dir since the same day. Under the premise, an install either
  has the logo in the data dir already or never had one in the release tree.
  A box outside the premise would show "no logo" in the designer and print
  receipts without it; the file itself is not deleted by this change.
- **`set -e` safety.** The removed block's replacements are `mkdir -p` and
  `chown -R` on a directory the line above creates; neither can fail on a
  fresh install or an upgrade. The Tester's real dpkg runs exit 0 both ways.
- **Docs.** `web/help/` has no topic that mentions the symlink or
  `/var/lib/unitill`. `README.md` did (finding R2). ut-docs has no other
  page describing it.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | minor | The comment above `chown -R pos:pos /var/lib/unitill` said the chown is "retained for legacy upgraded boxes" (Tester's nit). That is half the story and reads as if the line were removable: the directory is also where the kiosk first-boot marker lives on every box. It also did not say *why* older boxes need it. | **Fixed.** The comment now states both reasons: the directory holds `kiosk-setup-done`; and on a box first installed from an older `.deb`, dpkg keeps the items symlink and unpacks the demo thumbs through it as root, while `chown -R /opt/unitill` does not follow the symlink. The chown itself is kept. |
| R2 | minor | `README.md` Quick Start still said the `.deb` "keeps item photos in `/var/lib/unitill`". That stopped being where uploads go on 2026-07-29 and is now not provisioned at all. | **Fixed.** Now "keeps shop data and item photos in `/opt/unitill/data`" (the `UT_DATA_DIR` the shipped unit pins). |
| R3 | minor | The guard only matched the literal `ln -sfn /var/lib/unitill/items`. Bringing the block back with the target in a variable, or with `ln -s`, would pass. | **Fixed.** The test now fails on any code line naming either end of the old link (`/var/lib/unitill/items` or `public/assets/items`); the script has no other reason to name them. Three mutations checked (below). |
| R4 | info | On a box with the dpkg-preserved symlink, package item files keep landing in `/var/lib/unitill/items`, and an in-app self-update still replaces the link with a real directory. Nothing reads that tree for uploads, so nothing is lost; the directory just stays until purge (`postremove.sh` removes `/var/lib/unitill`). | Accepted. Cleaning it up would need a postinstall step that deletes under `/var/lib` — more risk than the leftover demo thumbs are worth. |
| R5 | nit | Pre-existing, outside this diff: `internal/pages/catalog/handlers.go` (comment above `POST /api/catalog/item/image`) still names `web/public/assets/items/<id>/thumb.png` as the upload path; ut-docs `reference/investigation-5058-backup-restore.md` lists the `.deb` data dir as `/var/lib/unitill` (it is `/opt/unitill/data`). | Not changed here; both predate #4401. Deferred (below). |

No blocker or major finding. No data-loss path found under the premise.

## TDD re-verification (reviewer, in person)

- Re-added `ITEMS=…` and `ln -sfn /var/lib/unitill/items "$ITEMS"` to
  `postinstall.sh` as committed → `go test ./packaging -run
  TestPostinstallLegacyItemsSymlinkRetired` **fails**
  (`postinstall_items_test.go:22: postinstall.sh recreates the legacy items
  symlink`). Restored the script byte-for-byte from a copy → passes.
- After strengthening the guard (R3), three mutations, each restored and
  compared with `cmp`: the literal `ln -sfn` line → fails;
  `ln -s "$T" /opt/unitill/web/public/assets/items` → fails (the committed
  guard would have passed this one); a comment naming
  `migrateLegacyUploadedAssets` → fails. Unmutated → passes.
- The Tester had independently run the first and third mutations.

## dpkg evidence (Architect and Tester, not re-run by me)

Real dpkg 1.22.6, real scripts: v1 = `origin/main` postinstall, v2 = this
branch's.

- Install v1: `web/public/assets/items` is a symlink onto
  `/var/lib/unitill/items`. An "uploaded" `itm999` is added there.
- Upgrade to v2: dpkg exits 0 with the postinstall under `set -e`. **dpkg
  keeps the symlink** and writes the package's item files through it; the
  demo thumb is updated, `itm999` survives, `/var/lib/unitill` and `itm999`
  are owned by `pos`.
- Purge, then fresh v2: exits 0; `items` is a real `pos`-owned directory
  holding the demo thumbs; `/var/lib/unitill` exists, `pos`-owned, with no
  `items` subdirectory.

My comment for R1 relies on this result; the script lines it describes were
not changed by my fixes.

## Gate (after the last edit)

- `gofmt -l .`: empty.
- `go build ./...`: ok. `go vet` on `internal/paths`, `packaging`,
  `internal/db`: ok.
- `go test ./internal/paths/... ./packaging/... -race`: ok.
- `go test ./internal/db/... -race -timeout 40m`: DB_RACE_RESULT
- `golangci-lint run ./...`: 0 issues.
- `sh -n` and `dash -n packaging/scripts/postinstall.sh`: ok.
  `TestPackagingShellScriptsParse`: ok.
- Guards run: mail-addresses, compliance-claims, competitor-naming,
  wip-commits: pass. The Tester ran data-access, i18n, fixed-dates,
  core-neutral, pipefail-grep-q, demo-env, netaccess before my fixes; mine
  touch a shell comment, a README sentence and one test.
- **Not run:** `shellcheck` — there is no binary in this sandbox and none
  was downloaded. The change removes lines and edits comments only; CI's
  shellcheck step is the check. The Tester's `go test ./... -race` timed out
  at the default 10 min on `catalogpull`, `catsync`, `cloudsync`, `data` and
  `db` (all pass without `-race`; none is touched except `db`'s comment).

Pre-flight: `web/help/` n/a (packaging-internal, no topic mentions it);
`README.md` updated (R2); ut-docs `architecture/packaging.md` updated (own
branch); `reference/` n/a for this change (R5 is a different, older
inaccuracy → deferred); `en.json` n/a (no strings); ADR n/a (retires a
transitional bridge as its own comment planned, no decision reversed);
review record: this file, plus a pointer in ut-docs `code-reviews/`.

**Verdict:** approve with fixes (R1–R3 applied in the working tree,
uncommitted).

**Deferred (Backlog cards to file at close-out):**
- R5a: correct the stale upload-path comment in
  `internal/pages/catalog/handlers.go`.
- R5b: correct the `.deb` data dir in ut-docs
  `reference/investigation-5058-backup-restore.md`.
