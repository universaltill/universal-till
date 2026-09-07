# Code review: internal/plugins CI test step — add `-v` so a skip is visible

- **Card:** universaltill/ut-docs#1626
- **PR:** universaltill/universal-till (branch `fix/1626-plugins-ci-test-verbose`)
- **Complexity:** easy — built at Sonnet (inline), reviewed by a fresh-context
  Sonnet subagent (per `MODEL-ROUTING.md`'s easy-tier review exception)
- **Date:** 2026-09-06

## What was wrong

`.github/workflows/ci.yml`'s "Test (internal/plugins — wider timeout,
ut-docs#643/#753/#776)" step ran `go test -timeout 20m ./internal/plugins`
with no `-v`. Plain (non-verbose) `go test` prints no `--- SKIP` line and no
skip count — a package containing a skipped test still reports a bare `ok`,
indistinguishable from every test having actually run and passed. This
surfaced during independent review of ut-docs#1198 (see that card's own
`2026-09-06-publisher-floor-skip-not-fail-1198.md` record), which changed
`TestReload_SurvivesRealisticPublisherContention`'s publisher-floor
precondition from a hard `t.Fatalf` to `t.Skip` under scheduler starvation —
a change whose whole safety premise depends on someone being able to see how
often the test actually goes inconclusive, which CI currently can't show.

Verified the gap was still live before touching anything: `git log` /
current `ci.yml` line 181 (pre-fix) read exactly
`go test -timeout 20m ./internal/plugins`, matching the issue's report.

## What changed

- Added `-v` to that one `go test` invocation
  (`.github/workflows/ci.yml`, the `internal/plugins` wider-timeout step
  only — the main `Test` step and the OS-locale step are untouched).
- Added a short comment above the `run:` line citing ut-docs#1626 and the
  concrete ut-docs#1198 example, so the next reader knows why this one step
  differs from the others.

Scope was deliberately kept to the exact step named in the issue — the
cheaper of the two options the issue itself suggested (the alternative,
post-processing `--- SKIP` into a job-summary annotation, is more machinery
for the same signal and wasn't warranted for one step).

## What the independent review found (fresh-context Sonnet subagent)

No blocking issues. Confirmed:
- Scoping is correct — `git diff --stat` shows exactly one file/step
  changed; the main `Test` step (line 150, excludes `internal/plugins` via
  `grep -v`) and the OS-locale step are untouched.
- The new comment's claim is accurate:
  `TestReload_SurvivesRealisticPublisherContention` in
  `internal/plugins/reload_busy_production_test.go` does `t.Skipf` on the
  ut-docs#1198 publisher-floor precondition, and the comment doesn't
  restate or conflict with the adjacent ut-docs#662 double-run comment
  (that one is about package-scoping, not verbosity).
- No material downside to `-v` on this step: it doesn't use `-race`, no
  `t.Parallel()` anywhere in the package (so no interleaved/misattributed
  `-v` output), no downstream consumer parses this step's log
  (no `-json`, no coverage scraping), and the log-volume increase is modest
  (~263 top-level test funcs, one `--- PASS`/`--- SKIP` line each).
- Confirmed CI-workflow-only: `git diff --stat` shows only `.github/
  workflows/ci.yml` changed, no Go source — so the repo's Go-specific gates
  (`gofmt`, `guard-data-access.sh`, `guard-i18n.sh`, etc.) aren't implicated.

**One non-blocking note, filed as a new Backlog card rather than widening
this PR:** `internal/plugins/oauth/token_client_test.go` and
`internal/plugins/marketplace/client_more_test.go` also contain `t.Skip`
calls and run under the *main* `Test` step (which only excludes the exact
`internal/plugins` package, not its subpackages) — so the identical
invisible-skip gap still exists there. ut-docs#1626 named only the
`internal/plugins` step specifically; the subpackage gap is now tracked as
universaltill/ut-docs#1630.

## Verification (beyond the reviewer's independent pass)

- `gofmt -l .` — clean (no output; this is a YAML-only change).
- `go test -timeout 20m -v ./internal/plugins` run directly: full package
  green (89.7s), `-v` output correctly shows `=== RUN` / `--- PASS` for
  every test — confirms the flag actually produces the intended visibility
  and doesn't change pass/fail outcome or blow past the 20m timeout.
- No Go source touched, so `go build ./...`, `golangci-lint run ./...`, and
  the repository-pattern/kiosk-engine/i18n/compliance guards are
  unaffected by this diff — confirmed by `git diff --stat` showing the sole
  changed file is `.github/workflows/ci.yml`.

## Safe-to-merge verdict

Safe to merge. No blocking issues found; one adjacent gap (subpackage
skips) is real but out of this card's scope and tracked separately as
ut-docs#1630.
