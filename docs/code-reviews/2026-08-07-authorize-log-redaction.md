# Code review — .authorize WASM event response log redaction

- **Date:** 2026-08-07
- **Task:** ut-docs#245 (wasm_runtime.go: .authorize event responses still
  log verbatim, same class as #202)
- **Branch:** `fix/245-authorize-log-redaction`
- **Author:** pipeline Dev step (Sonnet, inline)
- **Independent reviewer:** general-purpose subagent on **Opus** (different
  model, per standing practice — this card is `complexity:medium`)

## What shipped

`wasmResultLogLine` in `internal/plugins/wasm_runtime.go` redacts a WASM
plugin's stdout result before it reaches the log — but ut-docs#202 scoped
this to `.ask`-suffixed events only, explicitly leaving `.authorize`
events (payment-authorization plugins, `PublishAuthorize`/`Blocking`
hooks — see `Sync`'s event-mode branch in the same file) unredacted, even
though they are the same "blocking, value-returning hook" shape and
arguably the most credential-adjacent plugin output in the system
(transaction/auth tokens, card-present metadata).

- `wasmResultLogLine` now redacts both `.ask` and `.authorize` suffixes.
- `looksLikeBlobFieldName` (matched `b64` only) is renamed
  `looksLikeSensitiveFieldName` and also matches `token`/`secret`
  (case-insensitive substring) — per the ticket's acceptance criteria, a
  small token is exactly as much of a credential as a long one, so
  name-matching can't be size-gated.
- New unit tests: `TestWasmResultLogLine_GatesOnAuthorizeSuffix`,
  `TestSafeAskResultForLog_RedactsTokenOrSecretFieldByNameEvenWhenSmall`.
- New real end-to-end regression test
  `TestHandleEvent_AuthorizeResultRedactedInRealLog`, mirroring the
  existing `TestHandleEvent_AskResultRedactedInRealLog`: a real compiled
  WASI guest module (`testdata/bigauthorize_guest`, new fixture) run
  through the real wazero runtime and the real logging package, proving
  the actual call site is wired, not just the helper function in
  isolation — the acceptance criteria required this specific approach,
  since a prior review (#202) found a helper-only test leaves a wiring
  gap.
- `docs/wasm-runtime.md` (`ut-docs` repo) updated in the same session —
  the architecture doc's "not yet covered by this redaction" note about
  `.authorize` was actively wrong once this shipped.

## TDD evidence (independently re-verified, not just claimed)

Both halves of the change were regression-proofed by reverting each
independently and confirming the exact tests go red with the real leaked
value visible in real captured stdout, then confirming green again on the
real fix:
- Suffix gate reverted to `.ask`-only → `TestHandleEvent_AuthorizeResultRedactedInRealLog`
  and `TestWasmResultLogLine_GatesOnAuthorizeSuffix` fail, the former with
  the actual unredacted `auth_token` value visible in the captured log.
- Field-name matcher reverted to `{"b64"}` only →
  `TestSafeAskResultForLog_RedactsTokenOrSecretFieldByNameEvenWhenSmall`
  fails with the real unredacted token value.
- Full pre-existing `.ask`-log test suite re-run and confirmed unchanged
  (`TestWasmResultLogLine_GatesOnAskSuffix`,
  `TestHandleEvent_AskResultRedactedInRealLog`, all `TestSafeAskResultForLog_*`
  cases) — no behavior change for `.ask`.

## A process incident during review (disclosed, not hidden)

Mid-review, the reviewer's mandated revert-then-restore verification cycle
(temporarily reverting the fix to confirm a test goes red, then restoring)
ran in the same working-tree checkout this orchestrating session was
using. While the revert was live on disk, this session — reacting to the
environment's stop-hook requiring no uncommitted changes before a turn
ends — committed the checkout, capturing the **temporarily-reverted,
broken** `looksLikeSensitiveFieldName` (matcher narrowed back to `{"b64"}`
only) as commit `6b12605`. The reviewer's own report caught this by
diffing against its own pre-review checksum backup and flagged it.
**Fixed in-branch**: `git commit --amend` to include the correct
three-marker matcher, then the full suite (build/vet/gofmt/tests/guards)
was re-run against the corrected `HEAD` and confirmed clean before this PR
opened. Filed as its own process/pipeline-hazard card,
**universaltill/ut-docs#386**, since the fix belongs in the `reviewer`/
`scrum-master` skills (worktree isolation for mutate-then-restore review
subagents), not in this ticket's product code.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l` clean on all four changed/added files.
- `go test ./internal/plugins/...` (full package, not just the touched
  files) — clean.
- Full `go test ./...` — clean except the same pre-existing, unrelated
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` failure
  documented in prior review records (root-sandbox permission quirk).
- `bash scripts/ci/guard-data-access.sh` and `bash scripts/ci/guard-i18n.sh`
  — both green. No SQL involved in this diff at all; no i18n surface (a
  server log line, not a template/response string).
- `GOOS=wasip1 GOARCH=wasm go build ./internal/plugins/testdata/bigauthorize_guest`
  — confirmed the new guest fixture compiles standalone.
- No UI/template surface touched — no browser-driven check needed
  (backend-only: WASM runtime logging + its tests).

## Review findings

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | **blocking** | `HEAD` briefly contained a broken `looksLikeSensitiveFieldName` (matcher narrowed to `{"b64"}` only), from the concurrent-commit incident above | **Fixed** — `git commit --amend` with the correct matcher; full gate re-run and confirmed green on the corrected `HEAD` before opening this PR |
| 2 | should-fix | `docs/wasm-runtime.md` (ut-docs) still said `.authorize` events "are not yet covered by this redaction — tracked as a follow-up", now false | **Fixed** — doc updated in the same session, per this repo's `CLAUDE.md` non-negotiable; also notes the field-name matcher widening and the nested-field gap (#3 below) |
| 3 | should-fix (follow-up, not this diff) | `safeAskResultForLog` only inspects **top-level** JSON fields — a nested credential (e.g. a gateway SDK's `{"provider":{"auth_token":"..."}}`) is neither size- nor name-matched and logs verbatim, pre-existing since #202 but more likely to bite `.authorize` responses | **Carded** — ut-docs#384 (`p2`, `security`) |
| 4 | nit (follow-up) | The sibling blocking hook `payment.<key>.refund` (`internal/pages/refund_page.go`) goes through the same event seam but isn't gated by `wasmResultLogLine`'s `.ask`/`.authorize` suffix check, so a refund plugin's stdout still logs verbatim | **Carded** — ut-docs#385 |
| 5 | nit | The e2e test's large field trips the size cap regardless of the name matcher, so it doesn't independently prove the name-matcher's *wiring* (the pure-function test does prove the logic) | **Accepted as-is** — the size-cap path already exercises the exact same production call site (`safeAskResultForLog` checks both conditions together, not via separate code paths), so this is a coverage nicety, not a real wiring gap; not worth a second guest fixture for a nit |
| 6 | nit | `safeAskResultForLog`/`maxAskFieldBytes`/`maxAskLogBytes` are `Ask`-named but now also serve `.authorize` | **Accepted as-is** — the existing doc-comment already says "despite the name..."; a rename would churn two more test files for no behavioral gain |

Also confirmed clean: no dead code (`looksLikeBlobFieldName` has zero
remaining references after the rename); no new imports; the guest fixture
is correctly `//go:build wasip1`-tagged under `testdata/`, invisible to
host builds; repository pattern and money-type rules not implicated (no
SQL, no monetary fields in this diff).

## Verdict

**Safe to merge** on the corrected `HEAD` (`320ec57`). The one blocking
finding was a process incident, not a defect in the reviewed logic itself,
and is fixed and re-verified in-branch; the should-fix doc gap is fixed in
the same session; the two should-fix/nit follow-ups are carded
(ut-docs#384, ut-docs#385) rather than scope-creeping this ticket, and the
pipeline process hazard that caused the incident is carded separately
(ut-docs#386) for the skills that own it.
