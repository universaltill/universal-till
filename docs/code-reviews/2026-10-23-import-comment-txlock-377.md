# Review: import_page.go barcode-attach comment (ut-docs#377)

- Change: comment-only edit in `internal/pages/import_page.go` (~line 1177).
  The comment now attributes AddBarcode's BEGIN IMMEDIATE guarantee to the
  DSN-wide `_txlock=immediate` (ut-docs#311, `internal/db/db.go`) instead of
  a dedicated-connection mechanism. No functional change.
- Author: Sonnet 5.5 (cloud lane cloud-54). Reviewer: Opus 5.5, fresh context.
- Gates: `gofmt -l internal/pages` clean; `go build ./internal/pages/`;
  `go test ./internal/pages/ -run Import` ok; `guard-docs-shots.sh` passes
  (import_page.go is outside the screenshot surface hash, so no regen).
- Findings: see the PR thread; none outstanding at merge.
