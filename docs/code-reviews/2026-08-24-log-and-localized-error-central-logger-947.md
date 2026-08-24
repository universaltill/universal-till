# Code review: centralize LogAndLocalizedError on the app's leveled logger (ut-docs#947)

## What shipped

ut-docs#947 was filed from a code-review finding during the #316/#893/#924
`http.Error` i18n sweep, flagging three distinct, unrelated gaps in the
`common.LogAndLocalizedError`/`http.Error` infrastructure. BA scoping this
cycle split it three ways instead of building it as one card:

1. **Problem 2 (built this cycle)** — `LogAndLocalizedError` logged via
   stdlib `log.Printf("[%s] %v", logTag, err)` instead of the app's own
   leveled/structured logger (`internal/logging`). Fixed once, centrally,
   in the helper: `internal/pages/common/errors.go` now calls
   `logging.L().Errorf("[%s] %v", logTag, err)` — same format string, same
   argument order, so log-line *content* is unchanged; only the writer and
   level annotation change. All 101 non-test call sites across
   `internal/pages` inherit the fix automatically, with no per-site edits.

   Side effect, intended and documented in the diff's own comment: since
   this now logs at Error level through `logging.L()`, these lines also
   flow into `logging.Recent()` — the in-memory Problems ring the
   cloud-sync heartbeat reports (ADR-0018) and the `/backoffice` panel
   reads. Before this fix, an error routed through this helper was
   invisible to that feed even though it's exactly the class of
   server-side problem it exists to surface.

   `internal/pages/common/errors_test.go`'s `TestLogAndLocalizedErrorLogsTheRealError`
   was rewritten to assert via `logging.Recent()` (the sanctioned way to
   observe the leveled logger, per its own `ResetRecent` doc comment)
   instead of hijacking stdlib `log`'s global writer — `LogAndLocalizedError`
   no longer goes through stdlib `log` at all, so the old capture technique
   would have silently observed nothing.

2. **Problem 1 (decision, no code change)** — raw `err.Error()` recorded
   into the audit log at two sites in `internal/pages/backup_api.go`
   (`backup_failed`, `restore_stage_failed`). Judged **intentional**: the
   audit log is a manager/admin diagnostic surface, not "the operator's
   screen" `LogAndLocalizedError`'s docstring refers to — an admin
   reviewing a failed backup needs the real error. Documented in place
   with a code comment at both sites rather than an ADR (not an
   architectural decision, a call-site clarification).

3. **Problem 3 (split off)** — `http.Error`-based API responses not
   matching the mandated `{data,error}` envelope. Explicitly out of scope
   for this cycle (the original card itself flagged it as potentially
   large); split into **universaltill/ut-docs#953** for its own scoping
   pass before any implementation.

Files touched: `internal/pages/common/errors.go`,
`internal/pages/common/errors_test.go`, `internal/pages/backup_api.go`
(comments only, no behavior change there).

## Independent review

Opus, fresh context, isolated git worktree (complexity:medium →
Sonnet-builds/Opus-reviews per the model-routing rubric). Verdict:
**safe to merge**, no blocking issues.

**Fixed from review**: the helper's new comment originally said "32+"
call sites — the reviewer counted the real number
(`grep -rn "LogAndLocalizedError(" internal/ --include=*.go | grep -v _test | grep -v common/errors.go`)
and got **101**. Since the comment's whole point is to explain the
blast radius of the ADR-0018 side effect, the number needed to be
accurate. Fixed; full gate re-run clean after the edit.

**Left as a follow-up card, not fixed here**
(**universaltill/ut-docs#954**): of the 101 call sites, 18 report 4xx
statuses (form-validation errors, a normal refund-flow tender error,
etc.) — routinely operator-triggerable, not real problems, but they now
log at Error level and compete with genuine 5xx failures for the
Problems ring's 50-slot cap and the cloud digest's 20-slot cap. This is
an honest, documented consequence of the fix (not a bug in it), and
deriving the log level from the HTTP status is a reasonable-sized
follow-up, not a blocker to this diff.

**Independently re-verified, not taken on trust**: the reviewer reverted
only `errors.go` to its pre-diff version (keeping the new test), ran
`TestLogAndLocalizedErrorLogsTheRealError`, confirmed it fails with
exactly `logging.Recent() is empty; want the real error recorded` (and
that stdlib `log` visibly still emitted the line — proving the test
fails for the *right* reason, not a fluke), then restored the file and
confirmed the test passes again. This orchestrator independently ran the
same revert/restore sequence beforehand with the same result, so the TDD
claim was verified twice, by two different sessions.

**Import cycle and log-shape equivalence checked, not assumed**: `go list
-deps ./internal/logging` reaches only `internal/clock` — cannot reach
`internal/pages/common`, confirmed empirically by a clean
`go build ./...`. The format string and argument order are unchanged
(`"[%s] %v", logTag, err` in both the old and new call); only the
timestamp format and the `[LEVEL]` token differ, which is the intended
effect of switching writers.

**Problem 1's audit-gating claim checked on the merits, not taken on
faith**: `internal/pages/audit_page.go`'s browse and export handlers both
gate on `canPerform(d, r, "audit")`, and migration
`042_reports_audit_permissions.sql` seeds that permission to
manager/admin/super_admin only — cashier is explicitly denied. No other
reader of the audit rows exists outside `internal/data`. The
`backup_failed` site is additionally behind the elevation flow. The
"admin diagnostic surface, not the operator's screen" reasoning holds.

**Problem 3 confirmed genuinely untouched**: nothing in the diff writes
JSON or touches the `{data,error}` envelope; `backup_api.go`'s
`backup_failed` handler still replies with the same raw HTML fragment it
did before, exactly the defect #953 now owns.

**Sanity-checked all three `logging.Recent()` consumers**: `/backoffice`
(manager/admin/super_admin-gated), the cloud-sync heartbeat (the shop's
own tenant, ADR-0018), and the bug-report bundle (user-initiated
submission) — none of them puts a raw error in front of an unprivileged
operator. The HTTP response body written to the operator is unchanged in
every case; `LocalizedError` still emits only the translated key.

**`guard-docs-shots.sh` caught a real gap in this orchestrator's own
pre-push gate, corrected before merge**: neither this orchestrator's local
gate run nor the reviewer's ran `guard-docs-shots.sh` — both judged it
irrelevant since no template/UI file changed. That judgment was wrong:
the guard hashes the *whole* `internal/pages/common/errors.go` file (a
route-less shared helper falls into the "kept" bucket, per the guard's
own doc comment) as part of the app-surface hash regardless of whether
the specific lines changed are UI-adjacent, so touching it at all staled
`web/help/img/manifest.json`. CI's `build` job caught this on the actual
PR push and failed as designed. Fixed by running `make docs-shots` for
real (92 screenshots, Playwright/Chromium) and committing the regenerated
manifest; guard green after. Two PNGs
(`web/help/img/en/invoices.png`, `web/help/img/fa/translations.png`)
churned on regeneration — the same pre-existing screenshot-run
nondeterminism already documented in
`docs/code-reviews/2026-08-24-http-error-raw-leaks-924-increment4.md`
(unrelated to this diff, CI-invisible since the guard never hashes PNG
bytes) — reverted to keep the diff scoped to the genuine manifest change.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./internal/pages/...` — clean,
  both before and after the review-driven comment-count fix.
- `guard-docs-shots.sh` — red on first CI push (see above), green after
  a real `make docs-shots` regeneration and a follow-up gate re-run.
- Full test suite matching CI's actual invocation —
  `go test $(go list ./... | grep -v '/internal/plugins$')` and
  `go test -timeout 20m ./internal/plugins` — all green, post-fix. (An
  earlier `go test ./... -race` run without CI's per-package timeout
  override hit a 600s timeout in `internal/plugins`'s WASM-compilation
  test under the race detector — a self-inflicted invocation mismatch,
  not a real failure; re-run matching CI's own invocation was clean.)
- Guards run: `guard-data-access.sh`, `guard-i18n.sh`,
  `guard-kiosk-engine.sh` — all green (the only three plausibly relevant
  to this diff's scope; no template, route, plugin, or compliance-wording
  changes here).
- TDD claim re-verified independently twice across the pipeline: once by
  this orchestrator's own revert/restore pass, once by the Reviewer step
  in an isolated worktree.
- No real client/shop name, no secret-shaped literal, anywhere in the
  diff.
- No manual/help-topic update owed: zero files under `web/ui/`/`web/help/`
  touched, no route added or changed, no user-facing string added
  (`guard-i18n.sh` agrees) — this is server-side logging plumbing plus two
  code comments.

## Explicitly deferred (not fixed here, tracked separately)

1. **universaltill/ut-docs#953** — Problem 3, the `{data,error}` envelope
   migration scope decision. Untouched by this diff.
2. **universaltill/ut-docs#954** — 4xx call sites logging at Error level
   now compete with real 5xx problems for the Problems ring/digest's
   capped slots. An honest, documented side effect of this fix, not a
   defect in it; follow-up card opened from independent review.

## Safe-to-merge verdict

Safe to merge. The one fix independent review raised (an inaccurate
call-site count in a comment) applied and the full gate re-run clean;
all three relevant CI-blocking guards green; full test suite green
(matching CI's real invocation, including the isolated 20-minute
`internal/plugins` run); TDD claim re-verified independently twice
across the pipeline; both accompanying decisions (Problem 1 intentional,
Problem 3 deferred) checked on the merits rather than taken on the diff's
own word.
