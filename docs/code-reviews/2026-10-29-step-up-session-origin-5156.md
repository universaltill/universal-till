# Review — step-up PIN: session check and same-origin check (ut-docs#5156)

Date: 2026-10-29 · Branch: `fix/5156-step-up-session-origin` · Lane: `lane:cloud-41`

## What shipped

`checkStepUp` (`internal/pages/elevation.go`, ADR-0087) used to check only
the PIN. Any session could reach the device-wide `AuthorizeManager` lookup,
and there was no same-origin check. Every current caller (five Settings →
Data handlers and `POST /api/backup/restore`) already refused other sessions
with its own `canPerform` gate. The cloud-move approval POST planned in
ut-docs `reference/design-5127-till-approval-for-moves.md` §5 (fix M5) needs
both checks, so `checkStepUp` now runs them itself, before any PIN lookup:

1. **Same origin.** `sameOriginRequest` (new, `internal/pages/authz.go`)
   refuses a `Sec-Fetch-Site` other than `same-origin`, and an `Origin` that
   is not this request's own http(s) host. The logic was moved out of the
   cloud-pairing wizard's guard (`setup_cloud_pair.go`), which now calls the
   same helper. Requests without these headers are allowed, as before.
   Refusal: `errStepUpCrossOrigin` → "Approval isn't available on this
   device right now."
2. A nil `AuthSvc` still fails closed (checked before `canPerform`, which
   would otherwise nil-panic).
3. **The session holds the action.** Unless `UT_AUTH=off`, the session must
   pass `canPerform(action)`. The check is grant-based, so a custom role
   holding the grant passes (ADR-0128). A cashier session is refused without
   its PIN being checked, so it can neither burn the shared lockout nor use
   the endpoint as a PIN oracle. Refusal: `errStepUpSessionNotPermitted` →
   "Manager or admin required" (`common.error.manager_or_admin_required`,
   the message `requirePage` shows).

No new i18n keys. `checkOrElevate` is unchanged: its whole point is letting a
session without the right role continue with a manager's PIN. ADR-0087 has
an addendum for this (ut-docs branch `docs/5156-step-up-session-origin`).

## Tests

- `internal/pages/elevation_test.go`:
  - a cashier session with a valid manager PIN is refused, and 8 wrong PINs
    from it leave the lockout untouched;
  - auth on with no session is refused;
  - same-origin table: cross-site, same-site, `none`, a foreign Origin,
    `null`, `file://` and another port are refused; a matching Origin, a
    case-different Origin and no headers all pass;
  - an IPv6 host passes;
  - a custom role holding the grant elevates;
  - cross-origin wrong PINs don't burn the lockout;
  - `UT_AUTH=off` still elevates;
  - the error-key mapping.
- `internal/pages/data_api_test.go` `TestResetTransactions_CrossSite_ValidPIN_NoMutation`:
  a manager session (a real users row) with a valid PIN, sent cross-site,
  gets the prompt and the sale is still there.
- `e2e/tests/step-up-same-origin-5156.spec.ts` (auth project, real manager
  session, real Chromium): an in-page `fetch` with a valid PIN gets past the
  check, and the purge probe answers 404 (no such archive, nothing
  destroyed). The same PIN sent with `Sec-Fetch-Site: cross-site`,
  `same-site` or `Origin: http://evil.example` gets the prompt back with the
  "not available" message.

## Review

An independent Opus 5.5 reviewer read the Sonnet-built diff. No blockers.

1. **Major — fixed.** The first version also required `u.IsManager()` (role
   name). That shut out ADR-0128 custom roles holding the grant: Settings →
   Data shows them the tools (`isManager := canPerform(settings)`), but a
   manager's PIN would have been refused, where it used to work. Dropped;
   the gate is `canPerform` only. `TestCheckStepUp_CustomRoleWithGrant_Elevates`
   pins it (red with `IsManager` restored).
2. **Minor — fixed.** The session refusal reused "This PIN doesn't have
   permission", which blames the PIN. It now uses "Manager or admin
   required".
3. **Minor — fixed.** The cross-site handler test's session user had no
   users row. If the gate regressed, the reset would have failed on the
   audit FK, so the row-count assertion proved nothing. The test now uses a
   real users row. With the origin gate removed, the reset archives the sale
   and the test goes red.
4. **Nits — fixed.** `UT_AUTH=on` is now pinned in the session/origin tests,
   and port and IPv6 cases were added.

Checked by the reviewer, no change needed:
- The Android WebView, iOS WKWebView and desktop shell all load
  `http://<till>` and post same-origin. No custom-scheme page exists.
- Playwright's `request` sends neither header.
- README forbids a reverse proxy in front of the till.

**TDD re-verified by the reviewer.** The production change was reverted in a
separate worktree, with stub sentinels so the tests compile. The cashier,
no-session, same-origin, lockout, cross-site handler and error-key tests all
failed for the stated reason. The `UT_AUTH=off` control passed, as it should.
After restoring, everything passes. The two tests added after review were
checked the same way, inline.

## Gate

`gofmt -l .` empty, `go build ./...`, `golangci-lint run ./...` 0 issues,
`go test ./...`, every `ci.yml` build guard, and the e2e `auth` project
(login + this spec).

The docs-shots surface hash was refreshed: no rendered pixel changed.
`guard-deadcode-baseline.sh` fails in this container on `main` as well (the
tool there was built with go1.26, the module needs go1.27). shellcheck is not
installed here, and no shell script changed.

## Deferred

The `ut-plugin-language-*` packs still say "30 seconds" for
`auth.error.locked`. That value changes with #5141 (universal-till#2216, not
merged at the time of writing). It is a value refresh in the packs, tracked
by the follow-up card linked from #5156's close-out.

Verdict: safe to merge.
