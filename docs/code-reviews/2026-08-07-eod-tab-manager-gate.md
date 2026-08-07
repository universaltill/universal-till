# Code review: gate EOD reports tab's manager-only queries in Go (ut-docs#420)

**Date:** 2026-08-07
**Card:** universaltill/ut-docs#420
**Author (build):** scrum-master cycle, inline (Sonnet, `complexity:easy`)
**Reviewer:** independent fresh-context Sonnet subagent (different instance,
no prior context on this change — per `complexity:easy` routing)

## What shipped

`GET /ui/reports/tab/eod` (`internal/pages/reports_page.go`) ran
`ListArchivedReports` and two `Settings.Get` calls for every request, then
passed `IsManager` to the partial (`web/ui/partials/reports_tab_eod.html`),
which only *renders* the EOD controls inside `{{ if .IsManager }}`. A
non-manager got an empty section back — no data leak — but the queries
still ran for a role that can never see the result, unlike the idiom used
elsewhere in `internal/pages` (`isManagerOrAuthOff(r)` gating in the
handler before the repo calls, not just in the template). Not a security
bug — defense-in-depth/consistency, and two avoidable queries per
non-manager request.

Fix: `case "eod":` now computes `isManager := isManagerOrAuthOff(r)` up
front and only calls `repo.ListArchivedReports`/`d.Settings.Get` when
`isManager` is true. Values passed to `RenderPartial` are unchanged in
shape/name — `eodRows`/`eodEnabled`/`eodTime` just stay at their zero
values for a non-manager, same as what the template already rendered as
"nothing" either way.

## Independent review — findings

**No blocking issues.** Reviewer confirmed the entire partial body is
wrapped in one `{{ if .IsManager }}`, so skipping the queries cannot
change rendered output for either role, and cross-checked the approach
against ~90 other `isManagerOrAuthOff` call sites in `internal/pages` —
matches the established "gate expensive work, still render 200 with the
section omitted" idiom used by GET view/fragment handlers (as opposed to
the 403-early-return idiom used by API/mutation endpoints, which doesn't
apply here).

**Test-coverage judgment call, independently confirmed**: the card's
acceptance criteria asked for a regression case asserting no
archived-report query runs for a non-manager, "if that's cheaply
assertable." Both the implementer and the independent reviewer concluded
it isn't, given this codebase's test infrastructure — `openPagesTestDB`
opens a plain `*sql.DB` via `modernc.org/sqlite` with no query-spying/
counting wrapper anywhere in the repo, and there's no mocking dependency
in `go.mod`. Building one purely for this low-severity fix would be
disproportionate. The existing behavioral tests
(`TestReportsPage_ManagerOnlySectionsGatedByRole`,
`TestReportsPage_EODRowsOnlyIncludeEODKind`) already prove the rendered
output is correct/unchanged for both roles — accepted as adequate
coverage for a change that (deliberately) has no externally-observable
behavior difference, only a query-count one.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/... -run 'TestReportsPage|TestReportsTabs'` —
  all 16 tests pass unchanged, including the two manager-gating tests
  above.
- Full `go test ./...` — all packages pass except
  `internal/issuereport`'s `TestSaveCleansUpDirectoryOnWriteFailure`,
  confirmed pre-existing/unrelated (fails identically with this diff
  fully reverted; root-run sandbox artifact, already tracked as
  ut-docs#415).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  — both pass. Diff adds no raw SQL text (only calls existing repo
  methods), no user-facing string, no template change.
- No disk I/O, no real client/shop name, no literal secret anywhere in
  the diff.

## Not a visible/UI surface

The rendered output is byte-identical to before for both a manager and a
non-manager caller — confirmed by the reviewer reading the template and
by the unchanged existing tests. No manual/help topic update applies.

## Verdict

**Safe to merge.**
