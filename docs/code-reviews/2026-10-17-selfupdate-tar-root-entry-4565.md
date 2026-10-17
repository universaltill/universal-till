# Review — selfupdate accepts a bare "./" root entry (ut-docs#4565)

**Date:** 2026-10-17 · **Branch:** `fix/4565-selfupdate-tar-root-entry` ·
**Built by:** Sonnet (complexity:easy) · **Reviewed by:** Opus 5.5, fresh context

## What shipped

- `internal/selfupdate/selfupdate.go`: new `archiveEntryTarget` helper used
  by `extractTarGz`. A directory entry that cleans to the extraction root
  (`./`, as `tar czf X -C stage .` in `nightly.yml` writes first) is skipped;
  every other name goes through the same Join + prefix check as before, with
  the same `unsafe path in archive` error. A non-directory entry naming the
  root is still refused. It mirrors (does not import) `internal/plugins`'
  `archiveEntryTarget` from ut-docs#4563 — same accept/refuse set.
- `internal/selfupdate/archive_root_entry_4565_test.go`: a `./`-first
  tarball extracts; `.` and `a/..` as directory entries extract; `./` then
  `./../x`, `../x`, `../` and `.` as a regular file are refused and nothing
  lands outside `dst`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Accept test covered only `./`; `.`/`a/..` as dirs untested | Fixed — `TestExtractTarGz_AcceptsOtherRootDirSpellings_4565` |
| 2 | nit | No direct unit test of absolute/backslash names | Accepted — behaviour unchanged from `main` |
| 3 | nit | Comment quotes `tar -C dir -czf x.tgz .`, nightly.yml spells it differently | Accepted — same archive shape |

Reviewer's traversal analysis: nothing newly accepted except a root-equal
directory entry, which writes nothing; absolute, drive-letter and backslash
names behave exactly as before (also vetted with `GOOS=windows`); symlinks
and hardlinks remain ignored.

## Verified

- TDD re-verified by the orchestrator: with `main`'s `selfupdate.go` the
  accept test fails with `unsafe path in archive: ./`; with the fix it passes.
- `gofmt -l`, `go build ./...`, `go vet`, `go test -race ./internal/selfupdate/`,
  `golangci-lint run ./internal/selfupdate/...` (0 issues); all `ci.yml` guards
  pass locally except `guard-deadcode-baseline` (local deadcode binary built
  with go1.26) and `guard-shellcheck-version` (no shellcheck in the
  container) — both fail identically on `main` here; CI runs them.

## Verdict

Safe to merge. Not reachable in production today (goreleaser archives have
no `./` entry and nightlies have no `checksums.txt`), so no user-visible or
manual change.
