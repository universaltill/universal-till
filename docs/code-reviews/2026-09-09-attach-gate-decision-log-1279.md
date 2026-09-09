# Code review: log the attach-vs-spawn decision (ut-docs#1279)

**Branch:** `feat/1279-attach-gate-decision-log`
**Card:** ut-docs#1279 (complexity: easy) — review follow-up to ut-docs#1199
(`docs/code-reviews/2026-08-29-desktop-shell-attach-race-startup-gate.md`,
Finding 3).

## What shipped

`cmd/unitill-desktop/attach_gate.go`'s `waitForAttach` decides whether the
desktop shell attaches to an already-running systemd-managed till server or
gives up and spawns its own (the ut-docs#1199 fix). That decision was
previously silent — no log line at all — so diagnosing a recurrence of the
split-brain bug ut-docs#1199 fixed, or confirming the fix is working, needed
a hardware trip to inspect state directly.

This change adds exactly one stderr line per call to `waitForAttach`, logged
right before it returns (never per-probe, so a long retry window doesn't
spam the journal), covering:

- Whether the deadline gave the probe any chance to retry at all (a
  disabled/unreadable startup gate, `attach_gate_other.go`'s no-gate
  platforms, or a warm/manual launch already past the window all decide
  from a single probe, same as before ut-docs#1199) vs. a genuine retry
  window being open.
- The outcome (attached to an existing server, or giving up to spawn), how
  many probes it took, and roughly how long the decision took.

The message text is built by a new pure function, `attachDecisionLine`
(no clock, no I/O), mirroring `startup_gate.go`'s existing split between
`holdFor` (pure logic, tested) and `waitForSafeStartup` (the `Fprintf`
itself, untested) — the same precedent this change follows for message
style (`"<gate>: <details> (ut-docs#NNNN)\n"` to `os.Stderr`, logged once).

## Independent review

Spawned a fresh-context Sonnet subagent (complexity:easy → Sonnet review,
per `MODEL-ROUTING.md`), isolated in its own worktree, with no visibility
into the implementation reasoning. Verdict: **safe to merge as-is**, no
blocking findings, no nits.

What it verified independently (not taken on the implementer's word):

- **TDD claim**: reverted `attach_gate.go` to its pre-change state (keeping
  the new test), confirmed `TestAttachDecisionLine` fails to even compile
  (`undefined: attachDecisionLine`) — the genuine pre-fix state — then
  restored and confirmed the full suite passes again.
- **Two independent mutation checks** (different from the implementer's own
  outcome-string-swap check): flipping the `retryWindow` branch and
  breaking the pluralization guard both caused real test failures — the
  tests are not tautological.
- **Call-count purity**: diffed the pre- and post-change `waitForAttach`
  byte-for-byte and confirmed `probe()`/`sleep()` call sites and ordering
  are untouched; the 4 pre-existing tests in `attach_gate_test.go` (which
  assert exact probe/sleep call counts) pass **unmodified** — confirmed via
  `git diff HEAD^ HEAD -- attach_gate_test.go` showing only additive `+`
  lines, no `-` lines.
- **Log placement**: confirmed by reading the control flow that the two
  `fmt.Fprint` call sites are each immediately followed by `return`, so
  structurally at most one fires per call — no per-probe spam.
- **`guard-i18n.sh` scope**: read the script itself and confirmed it never
  scans `cmd/unitill-desktop/**` (only `web/ui/**/*.html`,
  `web/locales/*.json`, `internal/**/*.go`, and only `http.ResponseWriter`
  writes even within `internal/`) — this passes trivially and correctly,
  not as a near-miss.
- **No file I/O**: confirmed the diff contains no disk writes, only
  `os.Stderr`.
- **No secret-shaped literal / real shop name**: confirmed none present.
- **Manual/help-topic scope**: grepped for any path that pipes desktop
  stderr/journal output into the in-app bug-report feature
  (`internal/pages/issue_report_page.go`) and found none — this diagnostic
  never reaches a shop owner's screen, so no `web/help/` topic applies.
- **Diff scope**: confirmed via `git diff --name-only` that only
  `cmd/unitill-desktop/attach_gate.go` and `attach_gate_test.go` changed —
  no touch to `internal/pages`, `web/`, `internal/data`, `internal/money`,
  or plugin code.

## Verified beyond automated tests

- `gofmt -l cmd/unitill-desktop/` — empty.
- `go build ./...` — clean.
- `go vet ./cmd/unitill-desktop/...` and `go vet ./...` — clean.
- `go test ./cmd/unitill-desktop/... -v` — all tests pass, including the 5
  new `TestAttachDecisionLine` subtests and all 4 pre-existing
  `waitForAttach` tests, unmodified.
- Full `go test ./...` (69 packages) — all `ok` (run twice independently,
  once by Dev/Tester, once by the review subagent).
- `bash scripts/ci/guard-data-access.sh` — passes (no SQL touched).
- `bash scripts/ci/guard-i18n.sh` — passes, scope confirmed not to apply
  here (see above).
- `bash scripts/ci/guard-webkit-version.sh` — passes.
- `golangci-lint` and `bash scripts/ci/guard-deadcode-baseline.sh` — both
  confirmed **not applicable in this sandbox**: `cmd/unitill-desktop` is
  explicitly excluded from `golangci-lint` in `.golangci.yml` (pending a
  real `-tags=desktop` pass with GTK/WebKit headers, ut-docs#1581);
  `guard-deadcode-baseline.sh` needs a real cgo+GTK/WebKit build this
  sandbox lacks, and was confirmed to fail identically on unmodified
  `main` (not a regression from this change).

## Coverage gap, stated plainly

`waitForAttach`'s actual `os.Stderr` write is not itself captured by a
test — only the pure `attachDecisionLine` string-builder is tested
directly. This is a deliberate, accepted gap matching the existing
precedent in the same file (`waitForSafeStartup`'s `Fprintf` calls have no
dedicated test either, only its pure helpers `holdFor`/`gateDuration`/
`readUptimeFrom` do) — not an oversight.

## Verdict

Safe to merge. No blocking findings, no deferred items.
