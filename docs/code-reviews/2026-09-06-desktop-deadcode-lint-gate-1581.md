# Code review: whole-program deadcode baseline + desktop-tagged golangci-lint pass

- **Card:** universaltill/ut-docs#1581 (split from ut-docs#1565)
- **PR:** universaltill/universal-till (branch `feat/1581-desktop-deadcode-lint-gate`)
- **Complexity:** medium — built at Sonnet (inline), reviewed by an Opus subagent
  (`isolation: "worktree"`) per `MODEL-ROUTING.md`
- **Date:** 2026-09-06

## What was wrong

`.golangci.yml`'s `unused` gate (ut-docs#1565) deliberately excludes
`cmd/unitill-desktop`: type-checking it needs cgo + real GTK/WebKit dev
headers (only present in the `desktop-shell` CI job) and, without the
`desktop` build tag, an untagged pass false-positives on genuine
per-platform stub functions (`attach_gate_other.go`,
`startup_gate_other.go`, `shell_poll.go`'s two symbols — all only called
from their `desktop`-tagged sibling files). #1565 shipped the untagged
repo-wide gate and explicitly split out the desktop-tagged half as this
card, plus a companion whole-program `deadcode -tags=desktop` baseline (a
2026-09-04 measurement found ~97 pre-existing unreachable functions with
no gate at all).

A prior cloud cycle attempted this card and found its own sandbox
couldn't install `libgtk-3-dev`/`libwebkit2gtk-4.1-dev` (plain-HTTP
mirrors hung, an HTTPS mirror substitution got a 403) — verified, not
assumed, and documented in the card. **This session's environment does
not have that restriction** — `apt-get install libgtk-3-dev
libwebkit2gtk-4.1-dev` succeeded cleanly, `go build -tags=desktop
./cmd/unitill-desktop` and a real `deadcode -tags=desktop` run both work
— so the `blocked:env` concern in the card's body no longer applies here.

## What changed

- `scripts/ci/guard-deadcode-baseline.sh` (new): runs `deadcode
  -tags=desktop -test=false . ./cmd/unitill-desktop ./cmd/unitill-uninstall`
  (whole-program, all three shipped binary roots — the server main,
  the desktop shell, the uninstaller), normalizes `:line:col:` away,
  diffs against a checked-in baseline. New findings fail the build;
  entries that disappear (burned down, tracked separately as
  ut-docs#1566) don't force a baseline update.
- `scripts/ci/deadcode-baseline.txt` (new): seeded from a real run in
  this environment — 78 entries (fewer than the card's ~97 ballpark;
  #1565 already burned 4 down, and `main` has moved since the
  2026-09-04 measurement).
- `scripts/ci/guard-deadcode-baseline_test.sh` (new): regression test —
  plants a genuinely-new unreachable function, proves the guard rejects
  it, removes it, proves the guard accepts the clean baseline again.
- `.golangci-desktop.yml` (new): a second, narrow golangci-lint config
  (`unused` only, no path exclusions), meant to be invoked scoped to
  `./cmd/unitill-desktop/...` with `--build-tags=desktop`. Kept separate
  from the root `.golangci.yml`, whose exclusion must stay for the main
  `build` job's own untagged pass.
- `.github/workflows/ci.yml`: three new steps in the existing
  `desktop-shell` job (which already installs the GTK/WebKit headers) —
  the desktop-tagged lint pass, the deadcode baseline guard, and the
  guard's own regression test.
- `.gitignore`: the guard test's disposable fixture file.

## What the independent review found (Opus subagent, isolated worktree)

Ran the full mechanism for real (not just read the diff): installed the
headers itself, ran both new scripts, both new/existing lint
configurations, `go build`/`go vet` under the `desktop` tag, the full
`go test` suite (including `internal/plugins` at its 20m timeout), and
re-validated the workflow YAML. Everything passed. It also proved the
false-positive-avoidance rationale experimentally by re-running the
desktop-tagged lint config **without** `--build-tags=desktop`: the four
previously-known false positives (`attachDeadline`, `shellPollClientTimeout`,
`newShellPollClient`, `waitForSafeStartup`) reappeared, confirming the root
`.golangci.yml` exclusion is load-bearing and must stay. It also proved the
guard fails closed (a build broken by `CGO_ENABLED=0` makes the guard exit
1 with the compiler error, never a false "0 findings" pass).

It found three should-fix issues and three nits — all fixed in this
commit before merge, not filed as follow-ups, since each was cheap and
some were real correctness risks to a currently-passing gate:

1. **Should-fix (real bug, not yet triggered):** `grep 'unreachable
   func:' | sed ... | sort -u` under `set -euo pipefail` — the moment
   `deadcode` legitimately reports zero findings (the exact end state
   ut-docs#1566 is chartered to reach), `grep` exits 1 on no match,
   `pipefail` kills the script, and the guard dies silently with no
   error message at all. **Fixed:** `{ grep 'unreachable func:' ||
   true; }`. Verified directly: a repro with zero-match input now
   reaches past the grep with an empty result instead of exiting 1.
2. **Should-fix (mislabeling, real workflow-friction risk):** the
   analysis is necessarily whole-program (only 1 of 78 baseline entries
   is actually under `cmd/unitill-desktop/`; the other 77 are
   `internal/**` code, unreachable from either of the two original
   roots for legitimate structural reasons — `deadcode` needs real
   entry points, and scoping roots to `cmd/unitill-desktop` alone would
   make every `internal/` function only the server main calls look dead
   too), but the script's comments, error text, and the CI step name all
   said "cmd/unitill-desktop" specifically. Left as-is, a TDD-first dev
   adding an `internal/`-only helper covered solely by its own
   `_test.go` (invisible to `deadcode -test=false`) would get a
   `desktop-shell` failure naming a package their PR never touched.
   **Fixed:** reworded the guard's header comment, its success message,
   and the CI step name to state the whole-program scope plainly, and
   added an explicit note about the test-only-call-site false-positive
   shape (several existing baseline entries, e.g. `ResetCacheForTests`,
   look like exactly this).
3. **Should-fix:** `guard-deadcode-baseline_test.sh` was the only
   `guard-*_test.sh` in the repo not wired into CI (all 16 others are).
   Left unwired, a future edit to the guard's normalization logic could
   silently break detection with nothing to catch it. **Fixed:** added
   as its own `desktop-shell` step, right after the guard itself.
4. **Nit:** `cmd/unitill-uninstall` (a real shipped binary per
   `.goreleaser.yaml`) wasn't a `deadcode` root, so an `internal/`
   helper used only by it could false-positive as dead. **Fixed:**
   added as a third root — confirmed byte-identical output (still 78
   entries, no diff), so this repo has no such helper today.
5. **Nit:** the test script's comment named the disposable fixture file
   `.guard_test_fixture.go`; the actual constant is
   `cmd/unitill-desktop/zzz_guard_test_fixture.go`, and it wasn't
   gitignored. **Fixed:** corrected the comment and added the real path
   to `.gitignore`.
6. **Nit:** the two new CI steps didn't set `GOMODCACHE`/`GOCACHE` the
   way every sibling step in the job does, so they'd redownload modules
   within the same job run instead of reusing what Build/Vet already
   fetched. **Fixed:** added both env vars to all three new steps.

No TDD-revert verification applies here — this diff is CI tooling, not
a bug fix with a "confirmed-failing-before" claim; the regression test's
plant/reject/remove/accept sequence (case 2 above) is the equivalent
check for this diff, and it was run for real, not just asserted.

Two of the pipeline's standard watch-items were N/A: no file-write
handler in this diff (the guard's only writes are `/tmp/…$$` scratch
files, the same convention `guard-webkit-version_test.sh` already
uses), so no `os.MkdirAll` site to check; no cwd-relative path standing
in for `paths.Data(...)` (a Go-runtime concept this shell tooling
doesn't touch). No secret-shaped literals, no real client/shop name
anywhere in the diff.

## Verification (beyond the reviewer's independent pass)

- `gofmt -l .` (excluding `.claude/worktrees`) — clean.
- `go build ./...`, `go build -tags desktop ./cmd/unitill-desktop`,
  `go vet -tags desktop ./cmd/unitill-desktop` — clean.
- Root `golangci-lint run ./...` — 0 issues, `.golangci.yml`'s
  exclusion untouched by this diff.
- New `golangci-lint run --config=.golangci-desktop.yml
  --build-tags=desktop ./cmd/unitill-desktop/...` — 0 issues.
- `bash scripts/ci/guard-deadcode-baseline.sh` — passes against the
  live baseline, post-fix (whole-program message, `cmd/unitill-uninstall`
  included as a root).
- `bash scripts/ci/guard-deadcode-baseline_test.sh` — all 3 cases pass;
  fixture confirmed gitignored (`git check-ignore -v`) and absent from
  `git status` after a deliberate `touch`.
- Full `go test $(go list ./... | grep -v '/internal/plugins$')` plus
  `go test -timeout 20m ./internal/plugins` — green, matching CI's own
  invocation exactly (not `-race` across the whole repo: `internal/pages`
  hits the resource-contention timeout class already documented as
  ut-docs#1198's flake — confirmed by isolating the same test, which
  passes in 0.06s alone and the whole package in 67s without `-race`;
  CI itself never runs this suite under `-race`).
- `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml'))"`
  — valid, re-checked after every edit.
- Every other CI-blocking guard in `CLAUDE.md`'s "Before committing"
  list — ran all of them directly, all pass (data-access, kiosk-engine,
  plugin-menu-read, page-http-error, i18n, compliance-claims,
  docs-shots, help-topics, webkit-version, kiosk-launch-flags,
  android-status-address, android-i18n, emoji-font, htmx-loaded,
  autofill-suppression, e2e-fixtures-import, brand-assets,
  makefile-version) — none touched by this diff, confirmed unaffected.
- No UI surface, no user-facing string, no manual/help topic implicated
  — this is CI tooling only.

## Safe-to-merge verdict

Safe to merge. All six review findings fixed and re-verified in this
same commit; no blocker-class issue (money/tax, data loss, security)
was found, so per `MODEL-ROUTING.md` this stays a single review round.
ut-docs#1566 (burn down the 78 baseline entries) is unblocked to
proceed next, using `scripts/ci/deadcode-baseline.txt` as its checklist,
per this card's own sequencing note.
