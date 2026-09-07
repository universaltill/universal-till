# Code review: pairing_wait.html restart button error handling (ut-docs#1620)

- **Date**: 2026-09-06
- **Card**: universaltill/ut-docs#1620
- **Repo/branch**: `universal-till`, `fix/1620-pairing-restart-error-handling`
- **Complexity**: easy — Dev inline at Sonnet, review via a fresh-context
  Sonnet subagent (isolated worktree)

## What shipped

`web/ui/partials/pairing_wait.html`'s "joined" screen restart button wired
`htmx:afterRequest` to unconditionally call `waitForTill()` regardless of
response status. The manager-gated route (`POST /api/sync/pairing-restart`)
can answer 403 (the manager's session lapses between the join completing
and the click) or 409 (`pairingRestartHandler`'s own "nothing staged"
guard, on a stale double-click after a restart already happened) — either
way the script claimed "Finishing setup — the till is restarting…", polled
`/healthz`, got an immediate 200 from the still-running (unrestarted)
process, and redirected to `/login`, asserting a restart that never
happened. Same defect shape ut-docs#1613 fixed on the sibling
`backup_restore_staged.html` (found during that card's own review).

- `htmx:afterRequest` now branches on `ev.detail.successful`: only a real
  success polls; a non-2xx parses the response — the JSON `error` field
  for the 409 envelope, or the raw (trimmed) text for the plain-text 403
  `managerGate`/`common.LocalizedError` returns — and shows it with the
  button left live, using this app's existing `'✗ '` failure-text
  convention (`reports_tab_eod.html`, `plugins.html`, …).
- `htmx:responseError` (which used to also call `waitForTill()`
  unconditionally) is removed: it fires for exactly the non-2xx cases the
  new `afterRequest` branch now owns, and re-triggering the poll from it
  would undo the fix. `htmx:sendError` (a genuine network failure) is
  unchanged — it still resumes the poll, since that case means the till
  may already have restarted out from under the request.
- New locale key: none — reuses the pre-existing `common.error.server` as
  the fallback for an unparseable body, same as `backup_restore_staged.html`
  does not need one either.
- `docs-shots` regenerated (`web/ui/**` changed); only unrelated screenshots
  moved by a few bytes (encoder noise, same class documented in
  `2026-09-06-backup-restore-restart-1613.md`).

## Independent review — findings and disposition

Reviewed by a fresh-context Sonnet subagent (Dev also ran on Sonnet — this
card is `complexity:easy`, where "different model" relaxes to "different
instance" per the reviewer skill) in an isolated git worktree, instructed
to run everything itself and independently re-verify the TDD claim via
revert→confirm-real-failure→restore, not take it on faith.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| M1 | Major | The first draft of the new regression test only checked `strings.Contains(body, "ev.detail.successful")` — a token-presence check, not a direction check. The reviewer mutated the shipped fix to the deliberately inverted `if (!ev.detail.successful) { waitForTill(); return; }` (a success now shows an error, a real 403/409 now silently polls and redirects — the exact bug class this card exists to prevent, just flipped) and the test still **passed**. A textbook false-pass test, the same class this pipeline has caught before. | **Fixed**: the test now asserts the exact literal statement `if (ev.detail.successful) { waitForTill(); return; }` is present, and separately asserts `if (!ev.detail.successful)` is **absent** — re-verified by re-running the reviewer's own inversion mutation against the tightened test: it now fails with a real, specific message, and passes again once reverted. |
| Mi1 | Minor | `http.Error` (used by `managerGate`'s `common.LocalizedError` on a 403) appends a trailing `"\n"` to the response body, which the script's fallback branch put into `msg.textContent` unmodified (`"✗ Manager or admin required\n"`). Not visibly broken today (normal whitespace collapse hides it), but fragile against a future `white-space: pre-wrap` on that element. | **Fixed**: the fallback text is now `.trim()`-ed before use. |
| N1 | Nit | The first-boot flavour's `firstBootGate` refuses via a 303 redirect (not a 4xx), so XHR follows it transparently and htmx still sees `ev.detail.successful === true` on that path — the fix's `successful` branching doesn't cover that gate's refusal. | **Not fixed, correctly out of scope**: this was equally true before the fix (the old code polled unconditionally on every response including this one), it's the auto-firing first-boot wizard path (not the manager-gated 403/409 this card is about), and the reviewer confirmed it's pre-existing behaviour, not a regression. Noted here for the next person who touches `firstBootGate`'s response shape. |

Also independently re-verified rather than taken on faith:
- The JSON/plain-text assumption in the script is correct against the
  actual handler code: `pairingRestartHandler`'s 409 guard really answers
  `{"data":nil,"error":"<translated>"}` JSON; `managerGate`'s 403 really is
  plain text via `http.Error`. Confirmed live, not just by reading source.
- No file-write/`os.MkdirAll` or cwd-relative-path regression anywhere
  near this diff (verified via `git diff --name-only` — only the partial,
  the test file, and screenshots changed).
- `web/help/en/multitill.md`'s restart prose describes the happy path only
  and makes no claim this diff would make false — no manual update needed.
- No real client/shop name or secret-shaped literal introduced.
- Structurally cross-checked against `backup_restore_staged.html`
  (ut-docs#1613, not yet merged to `main` at review time): same
  `afterRequest`/`sendError` wiring shape, same `'✗ '` convention, same
  JSON/plain-text handling.

## TDD re-verification (revert → confirm real failure → restore)

Performed twice — once by the review subagent on the original fix, once by
me on the tightened test after M1's fix:

1. Reverted `web/ui/partials/pairing_wait.html` to the pre-fix version
   (`git apply -R` on just that file).
2. `go test ./internal/pages/... -run TestPairingWait_JoinedRestartScriptHandlesNonSuccessResponse -v`
   → **FAIL**, real specific message quoting the still-unconditional
   `btn.addEventListener('htmx:afterRequest', function () { waitForTill(); });`.
3. Restored the fix, re-ran → **PASS**.
4. Separately, applied the reviewer's own inversion mutation
   (`if (ev.detail.successful)` → `if (!ev.detail.successful)`) against the
   *tightened* test → **FAIL** (closing the M1 false-pass gap); reverted the
   mutation → **PASS** again, byte-identical to the pre-mutation file.

## Verified beyond automated tests

- `gofmt -l .` clean, `go build ./...`, `go vet ./...` clean.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- `go test ./internal/pages/...` — full package green (68–73s across runs).
- `bash scripts/ci/guard-i18n.sh`, `guard-htmx-loaded.sh`,
  `guard-page-http-error.sh`, `guard-docs-shots.sh` — all green.
- `make docs-shots` (via `e2e/scripts/docs-shots.sh`, reusing this
  session's pre-installed Chromium per ut-docs#622) — 100/100 screenshots
  captured; only unrelated screens moved by a few bytes (encoder noise).
- Git identity on every commit checked before committing: real GitHub-
  linked address (`4035824+farshidmirza@users.noreply.github.com`), never
  an AI-tool identity.

## Explicitly deferred (not silently dropped)

- N1 above (`firstBootGate`'s 303-redirect shape not covered by
  `successful` branching) — pre-existing, out of scope for this card, left
  as a note for whoever next touches that gate's response shape.

## Verdict

**Safe to merge.** The one Major finding (a false-pass test, not a logic
defect in the shipped fix) is fixed and re-verified; the Minor finding is
fixed; the Nit is correctly out of scope and noted. Full gate green.
