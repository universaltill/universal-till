# Review: stale item-photo and .deb data-dir paths (ut-docs#6188)

**Change.** Comment/doc text only, no behaviour change (ut-docs#6188, follow-up of #4401 review finding R5).
- universal-till `internal/pages/catalog/handlers.go`: the item image upload comment now says the photo goes to `<data dir>/public/assets/items/<id>/thumb.png` via `paths.Data` (`StoreItemPhoto` → `itemThumbFile`), served as `ItemThumbURL`; never the legacy `web/public` tree.
- ut-docs `reference/investigation-5058-backup-restore.md` §1.1: `.deb` data dir is `/opt/unitill/data` (`UT_DATA_DIR`, `packaging/systemd/unitill-pos.service:13`); `/var/lib/unitill` holds only the kiosk `kiosk-setup-done` marker and, on pre-#4401 boxes, the old items symlink target. The `config.go` citation moved from `:251` to `:254` (the `DataDir:` line today).

**Review.** Independent different-model review (Sonnet subagent) verified every claim against the code: `itemThumbFile`/`ItemThumbURL`/`StoreItemPhoto`, the service file line, and that nothing else under `packaging/` or `cmd/` writes to `/var/lib/unitill` (uninstall/postremove only clean it up). No blocking findings. Optional finding 1 (stale `config.go:251` citation) fixed; finding 2 (split the long table cell) declined — cosmetic.

**Verification.** `gofmt -l internal/pages/catalog/` clean; `go build ./internal/pages/...` passes. No test applies to a comment.

Pre-flight: help topic n/a (no visible behaviour) · `reference/` updated (investigation-5058) · README n/a · en.json n/a (no key) · review record: this file (one per repo) · follow-ups none.

**CI follow-up.** `guard-docs-shots_test.sh` failed on the first push: the comment edit changed the whole-file surface hash. Refreshed only `surface_sha256` with `scripts/ci/update-docs-shots-surface-hash.sh` (ut-docs#2102 escape hatch; comment-only, no rendered pixel changes), committed with `Docs-Shots-Unchanged: true`. Guard and its test pass locally.
