# 2026-11-04 — guard against fixed dates in test fixtures (ut-docs#5467)

**What shipped.** `scripts/ci/guard-fixed-dates.sh` flags every `YYYY-MM-DD`
in a `*_test.go` or `*.spec.ts` line (Go string, raw string, SQL inside a raw
string, Sprintf format) that falls in today..today+60 days, UTC, inclusive,
unless the line carries `fixed-date:ok <reason>`. Comment-only lines are
skipped. `UT_FIXED_DATE_TODAY` pins today for the guard's own
`guard-fixed-dates_test.sh`, which runs in the guard sandbox. Both run in
`ci.yml`'s `build` job. A new `fixed-date-window.yml` runs the guard daily,
plus on manual dispatch (`contents: read`, never a required check), because
`main` has no scheduled test run. A date entering the window therefore shows
up within a day, about 60 days before it can detonate. 21 in-window fixture
lines got reasoned markers. CLAUDE.md gains a "Test fixtures" rule. Runtime
behaviour is unchanged.

**Pre-flight:** web/help topic: n/a (no shop-owner-visible change).
`make docs-shots`: n/a (only test files changed under `internal/pages`;
the surface hash excludes `_test.go`). reference/ page: ut-docs
`reference/coding-standards.md` §8 bullet (separate ut-docs PR). README:
n/a. en.json keys: n/a. Lang packs: n/a. ADR: n/a (existing guard pattern).
Review record: this file.

**Trace (BA).** None of the three dates named on the card is a time bomb.
`subscription_check_test.go:52`, `entitlement_api_test.go:91,99` and
`replica_test.go:514,535` are all `entitlement.expires_at`, which is
display-only (ADR-0060 §5, `internal/entitlement/entitlement.go:43-45`).
Grace and active checks use `last_confirmed_at`, which those fixtures
already set to `time.Now()`. The BA proved this by backdating all of them to
2001-01-01: the tests still passed. The one real bomb the sweep found,
`internal/cloudsync/pending_moves_5127_test.go`, was fixed on `main`
meanwhile by universal-till#2343 (ut-docs#5471). The marker at
`move_5127_test.go:537` was dropped on rebase because #2343 made that
date relative.

**Review** (independent, read-only, Opus; Dev was Sonnet). No blockers.
- F1 (low) — fixed. A tracked `_test.go` deleted in the worktree made awk
  fail without printing an `::error`. Fix: skip a missing file, plus a test.
- F2 (low) — fixed. A block comment's middle ` * …` line was flagged. Lines
  starting with `/*` or `*` are now skipped as comments, plus tests.
  Accepted residual: a Go line that starts with a pointer dereference
  (`*p = "2026-12-01"`) is skipped too. That is rare, and a missed hit
  costs only an early warning.
- F3 (low) — fixed. The scope `e2e/**/*.spec.ts` missed
  `tests/e2e/tests/*.spec.ts`. It is now `*.spec.ts`, plus a test.
- Documented gap: `time.Date(2027, 1, 1, …)` is not scanned
  (`report_window_test.go:113` uses a fixed anchor and reads no clock).
- The reviewer spot-checked 8 marker reasons against the code under test,
  and all were true.

**Tester** (Opus; not the Dev):
- The guard test caught 14 of 17 mutations of the guard.
- Planted negatives behaved as specified. With
  `UT_FIXED_DATE_TODAY=2026-11-14` nothing fires; with 2027-04-02 it fires
  on `my_reports_page_test.go`, as the BA predicted.
- The word-diff shows the markers are comment-only. gofmt, vet, both YAML
  files and `guard-ci-tests-sandboxed` are clean.
- Findings:
  - F1 (high) — fixed. `shellcheck scripts/ci/*.sh` failed on SC1091 and
    SC2119. Added `# shellcheck source=` and
    `enter_guard_test_sandbox scripts/ci`.
  - F2 (high) — fixed. The branch needed a rebase onto #2343.
  - F3 (low) — partly fixed. Added a test for an untracked, not-ignored
    fixture. The BSD `date` fallback stays read-reviewed only.

**Gates (orchestrator, after the last edit):** see the PR. The guard test,
the guard on the real tree, `shellcheck scripts/ci/*.sh`, gofmt, vet and the
touched packages' tests were run after the rebase.
