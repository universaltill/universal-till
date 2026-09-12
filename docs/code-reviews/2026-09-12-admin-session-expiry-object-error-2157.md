# Code review: admin pages render `[object Object]` on an expired session (ut-docs#2157)

**Date:** 2026-09-12
**Card:** ut-docs#2157
**Complexity:** easy
**Author (build):** Sonnet (this pipeline cycle, `lane:cloud-54`)
**Reviewer (independent):** fresh-context Sonnet subagent, isolated worktree

## What shipped

Six admin pages made raw `fetch()` calls (no htmx) against session-gated
`/api/*` endpoints. `internal/auth/middleware.go`'s auth middleware answers
a non-htmx caller on an expired session with a bare 401 JSON envelope whose
`error` field is the object `{code, message}` — never the plain string
these pages' own endpoints normally return. Several of them then rendered
that object directly into a status message, producing the literal string
`"[object Object]"`; others silently misread the 401 as an empty result
("no matches", "none found", "nothing to remove") instead of either.
`ut-docs#2144` (already merged) fixed the equivalent htmx-driven case via
`HX-Redirect: /login`; this card is the raw-fetch admin-page counterpart the
middleware fix structurally cannot reach.

Fix: check `response.status === 401` immediately after each fetch, before
touching the body at all, and `window.location.href = '/login'` — same
established idiom `app.js`'s tender-panel handler already uses (ut-docs#2144).

Files touched:
- `web/public/app.js` — `utPostWithElevation`'s shared `send()` function
  (one fix covers all its callers: `settings.html`'s customer-erase and
  cleanup-catalog actions, plus `reports_tab_eod.html`).
- `web/ui/pages/plugins.html` — `window.act` and the plugin-import form
  handler.
- `web/ui/pages/catalog.html` — barcode-lookup autofill and the barcode
  keypad-capture handler.
- `web/ui/pages/tills.html` — LAN primary-discovery.
- `web/ui/pages/bluetooth_devices.html` — the shared `post()` helper (pair,
  forget) and the scan handler.
- `web/ui/pages/promotions.html` and `web/ui/pages/settings.html` — the
  customer-search/obsolete-items-preview GETs.
- `e2e/tests/session-expiry-redirect-admin-2157.spec.ts` (new) — one real
  regression test per distinct mechanism (the shared `utPostWithElevation`
  helper via `page.evaluate()`, `window.act` via `page.evaluate()`, and one
  real UI-driven click/keystroke per page-local closure), on the `auth`
  Playwright project (a real session that can actually expire).
- `e2e/playwright.config.ts` / `scripts/ci/guard-e2e-fixtures-import.sh` —
  routed the new spec to the `auth` project and exempted it from the
  `fixtures.ts` basket-reset wrapper, for the identical reason
  `session-expiry-redirect-2144.spec.ts` already is (a bare, cookie-less
  reset request would itself 401 against a revoked session).
- `web/help/img/manifest.json` — `surface_sha256` refreshed via the
  documented escape hatch (`scripts/ci/update-docs-shots-surface-hash.sh`):
  every changed line is inside an inline `<script>` error-handling path
  that only executes on a 401/500 response, so no rendered pixel changes.

## Independent review — findings

A fresh-context Sonnet subagent reviewed the diff in an isolated worktree,
ran the full gate itself, and independently re-verified the TDD claim by
reverting the six behavior-fix files to `origin/main`, re-running the new
e2e spec (all 6 failed, each with a `page.waitForURL` timeout on `/login`
— i.e. genuinely no redirect happens pre-fix), then restoring the fix and
confirming all 6 pass again identically.

**Verdict: safe to merge with one should-fix applied** (below); the rest
were nits or confirmed non-issues.

1. **should-fix, fixed.** `tills.html`, `promotions.html` and
   `settings.html`'s customer-search/obsolete-items handlers never checked
   `j.error` on a *non-401* error response — only `j.data`. For
   `/api/data/customers` and `/api/data/obsolete-items` (both answer real
   errors via `dataAPIRespond`, which writes a genuine `{"data":null,
   "error":"<string>"}` JSON envelope — confirmed by reading
   `internal/pages/data_api.go`), a real 403/500 was silently
   misrepresented as "no matches" / "nothing to remove" instead of a real
   error message — exactly the collapse this card's own AC #2 forbids.
   Fixed by adding `if (j.error) { renderNotice(<msg-el>, 'error',
   j.error); return; }` in all three handlers, reusing the exact
   `renderNotice(msg, 'error', ...)` pattern each of these files' own
   `.catch()` blocks already use for a network-level failure — `j.error`
   here is always a plain string (never the auth middleware's `{code,
   message}` object, which is now caught and redirected before this code
   ever runs), so this is a safe, direct `renderNotice` call, not another
   `[object Object]` risk.
   **`tills.html`'s `discover-primaries` needed no equivalent fix** —
   verified by reading `internal/pages/discovery_api.go`:
   `discoverPrimariesHandler`'s only error paths use `http.Error`/
   `common.LocalizedError` (plain **text**, not JSON, deliberately — see
   that handler's own ut-docs#303/#538 comment on never putting a raw
   error in a JSON body), so `r.json()` itself already throws for any real
   error there and correctly falls into the existing `.catch()` →
   `i18n.error` path, both before and after this diff. The reviewer's
   finding was accurate for the three `/api/data/*` call sites and did not
   generalize to `discover-primaries`'s different (plain-text) error
   contract — checked directly against the handler rather than assumed.

2. **nit, accepted as-is.** On the 401 branch, a mid-flight button/message
   (`btn.disabled = true`, `msg.textContent = i18n.searching`) is never
   explicitly reset before the redirect fires. Purely cosmetic —
   `window.location.href = '/login'` navigates away within the same tick —
   and every other 401 branch in this diff has the identical shape by
   design (the whole point is "stop here, leave"), so fixing this one
   handler alone would be inconsistent without touching all of them for a
   cosmetic non-issue. Not fixed.

3. **nit, accepted.** The final commit carries a `Docs-Shots-Unchanged:
   true` trailer per `guard-docs-shots.sh`'s own escape-hatch convention
   (not enforced by the guard itself, but the documented convention for
   this exact case).

Everything else the reviewer checked came back clean: `bluetooth_devices.html`
correctly guards all three call sites sharing the `post()` helper (pair,
forget) plus the separate `scan` fetch; `utPostWithElevation`'s fix
correctly covers all real callers (verified against every file that
imports it); the e2e technique (`page.evaluate()` for two of six tests,
real UI interaction for the other four) is a legitimate, precedented
shortcut — the two chosen for direct-function-call testing are exposed on
`window` and shared across multiple call sites, so testing them directly
is stronger evidence than one more UI click would be; forcibly clearing
`#bt-scan-btn`'s `disabled` attribute in the bluetooth test is a
reasonable, explicitly-commented way to route around this sandbox's
absent Bluetooth hardware (server-side `disabled` there is orthogonal to
session state); the `guard-e2e-fixtures-import.sh` exemption's reasoning
holds; the `manifest.json` surface-hash bump is justified (no markup/CSS
touched, confirmed by the guard's own independent hash check); no secrets,
no real client/shop name (uses `e2e-nonexistent`); no Go files touched (the
two recurring `os.MkdirAll`/`paths.Data` bug classes don't apply).

## Verified beyond automated tests

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean,
  `golangci-lint run ./...` — 0 issues.
- `go test ./...` — full suite green except one unrelated, pre-existing
  flake (`internal/procrestart`'s `TestRestartSchedulesDelayedReexecOfOwnExecutable`,
  a timing-sensitive assertion that fails only under heavy parallel `go
  test ./...` CPU contention from unrelated packages; reproduced passing
  reliably 5/5 in isolation, `go test ./internal/procrestart/... -count=5`).
  Zero Go files are touched by this diff, so this is definitively not a
  regression from this change.
- `bash scripts/ci/guard-i18n.sh` — clean (no new hardcoded strings; the
  fix uses no user-facing text at all — a bare redirect — and the one new
  `renderNotice(...)` call site each reuses `j.error`, an existing
  server-rendered string, not a literal).
- `bash scripts/ci/guard-e2e-fixtures-import.sh`,
  `bash scripts/ci/guard-docs-shots.sh`, `bash
  scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`,
  `guard-compliance-claims.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` — all green.
- **Real, personally-verified TDD**: reverted the six behavior-fix files to
  `origin/main`, re-ran the new e2e spec — all 6 tests failed with a
  `page.waitForURL` timeout waiting on `/login` (i.e. genuinely no
  redirect, not an unrelated tooling error) — then restored the fix and
  confirmed all 6 pass again. Independently repeated by the review
  subagent in its own isolated worktree with an identical result.
- e2e regression run (`--project=auth
  session-expiry-redirect-admin-2157`): 6/6 pass, both before and after
  the AC #2 follow-up fix above.
- No regression in existing coverage for the touched pages: re-ran
  `catalog-barcode-backfill-1356`, `catalog-import-friendly-errors`,
  `tills-lan-discovery`, `bluetooth-devices-76` on the `default` project —
  4/4 pass.
- **CI-caught regression, found and fixed after the first push**: the new
  spec was originally named `admin-session-expiry-redirect-2157.spec.ts`,
  which sorts alphabetically *before* `login.spec.ts` — Playwright runs a
  project's spec files in filename order, the `auth` project's server is
  shared across every file in it (`workers: 1`), and `login.spec.ts`'s own
  first test requires that shared server to still be genuinely
  unconfigured when it runs. This spec's `ensureOperator()` completes the
  full first-boot setup wizard on its very first call, so running first it
  silently consumed the fresh-install state `login.spec.ts` needed —
  caught by CI's `playwright` check (`login.spec.ts:29` failed both
  attempts: expected `/setup`, got `/login`), not by any local run before
  the first push (this failure mode only reproduces when both spec files
  run together in the real `auth`-project ordering, which the local runs
  up to that point had not exercised). Fixed by renaming to
  `session-expiry-redirect-admin-2157.spec.ts` (sorts after `login`,
  matching every sibling `AUTH_ONLY_SPECS` exemption's own naming) and
  documenting the ordering constraint both in the spec file's own header
  and in `guard-e2e-fixtures-import.sh`'s exemption comment. Re-verified
  by running the full `auth` project's real spec-file order locally after
  the rename: 26/26 pass, `login.spec.ts` first and green.
- No help-manual update needed: this is an internal error-handling fix on
  the session-expiry path, not a change to any screen a shop owner sees or
  a step they follow in normal use — same scope as the ut-docs#2144
  precedent, which also touched no `web/help/` prose.

## Deferred / explicitly out of scope

- The cosmetic mid-flight-state-on-redirect nit above (see finding 2).
- No further re-plumbing of the auth middleware itself — non-goal per the
  card, and correctly so: it already covers every htmx-driven call app-wide.

## Verdict

Safe to merge.
