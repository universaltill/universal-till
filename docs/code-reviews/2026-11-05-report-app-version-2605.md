# Review: issue reports record the till's app version (ut-docs#2605)

**What shipped:** `issuereport.Meta.AppVersion` is set from `buildinfo.Version` in `Save`, at capture time. A till that updates itself while offline therefore still tags an older report with the build that produced it. `uploadIssueReport` sends `app_version` only when it is non-empty, so bundles saved before this change are never back-filled with the current build. ut-cloud stores and filters on the field (ut-cloud PR for the same card, review record `docs/code-reviews/2026-11-05-bug-report-app-version-2605.md` there). The ingest there never rejects a report over its version, so a report cannot get stuck retrying (offline-first).

Built by Sonnet. Reviewed by an independent Opus 5.5 subagent; a separate Tester pass also ran.

## Findings
None in this repo. The reviewer's one code finding (a non-idempotent version normaliser) was in ut-cloud and is fixed there.

## Verification
- Reviewer re-verified TDD in a worktree. `TestSaveRecordsAppVersion` fails without the stamp in `Save`. `TestUploadIssueReportSendsAppVersion` fails ("9.9.9 want 0.31.2") when upload sends the current build instead of the captured one, which proves the test pins capture time. Both pass once restored. `TestUploadIssueReportOmitsAppVersionForOldBundle` covers old bundles.
- `gofmt -l .` clean, `go build ./...`, `go vet`, and `go test` of `internal/issuereport`, `internal/cloudsync`, `internal/buildinfo`; `golangci-lint run ./...` 0 issues. The Tester ran the full `go test ./...` (see the card's close-out).
- No signed or hashed payload covers the meta. The bundle write already uses its existing data directory, so there is no new path or `MkdirAll` concern.

Pre-flight: review record updated; help topic n/a (no user-visible behaviour on the till); en.json n/a (no strings); reference n/a; README n/a.

Verdict: approve, merge once CI is green.
