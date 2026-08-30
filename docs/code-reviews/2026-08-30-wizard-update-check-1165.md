# Setup wizard: update check on step 1, before setup continues (ut-docs#1165)

**Card:** ut-docs#1165 — product owner request, 2026-08-27. **Complexity:** medium.
**Dev:** Sonnet (subagent). **Review:** Opus (two rounds — see below).

## What shipped

The setup wizard's first screen (step 1) now checks in the background
whether a newer release exists and, if so, offers to update before setup
continues. Accept applies in place (re-exec, return to the wizard on the
new version); decline (or just not clicking) continues on the current
version. Never automatic, never blocking, never an error on an offline or
unreachable check.

Reuses the existing mechanisms end to end — no second copy of any of it:

- `internal/updates.CheckNow`/`Current` (the release check, 10s timeout,
  offline-first contract).
- `internal/selfupdate.Supported`/`Apply` (the in-place update itself).
- `internal/pages/update_api.go`'s `updateUnavailableHTML` (the
  can't-apply-here fallback with a manual download link).

New: two auth-exempt, `NeedsFirstBoot`-gated endpoints —
`POST /api/setup/update-check` and `POST /api/setup/update-apply` — mirroring
`setupLanguageInstallHandler`'s established pattern, because the existing
`/api/update/check`/`/api/update/apply` are gated by
`canPerform(d, r, "plugin_management")`, which no pre-auth first-boot
session can satisfy. Step 1 (`web/ui/pages/setup.html`) wires an
`hx-trigger="load"` container to the check endpoint — the wizard paints
immediately regardless of network state, the check happens as a background
swap.

3 new locale keys (`setup.update.prompt`, `setup.update.restarting`,
`setup.update.restart_timeout`, plus `setup.update.apply_failed` from the
first pass) across all four shipped locales (en/ar/fa/tr); `web/help/*/users.md`
updated with a new bullet describing the behavior; manual screenshots
regenerated (`make docs-shots`).

## Independent review — round 1 (Opus, worktree-isolated)

Verdict: safe to merge after one blocking fix. TDD claim independently
re-verified (moved the new handler file aside, confirmed the test suite
fails to *compile* with the exact `undefined: setupUpdate*` errors the dev
reported, restored, confirmed green).

**Blocking (B1) — fixed.** The `hx-trigger="load"` container and the
banner's Update-now button both sit inside the wizard's own `<form>`
(spans step 1 through the PIN step), and Alpine's `x-show` only hides via
CSS — so an operator who steps forward to the PIN step then back to step 1
has the PIN fields live in the DOM. htmx defaults to including the closest
enclosing form's fields on any non-GET request, so clicking Update-now
could have sent an admin PIN to this unauthenticated endpoint. Neither
handler reads the request body, so there was no functional bug, but it put
a credential where a proxy log or devtools capture could see it. **Fix:**
`hx-params="none"` on both the container and the button.

Non-blocking, addressed:

- **N1** — the "Updating and restarting…" message had no recovery
  affordance if the post-restart `/healthz` poll never came back (unlike
  the Settings page's own equivalent button, which offers "click to
  reload" after ~3 minutes) — worse here, since a kiosk till may have no
  other refresh control at all. Fixed with the same recovery pattern, new
  translated locale key (not the pre-existing `i18n:ignore` debt the
  Settings version carries).
- **N2** — the auto-triggered background check ignored `UT_UPDATE_CHECK=0`
  (the documented air-gapped opt-out); unlike Settings' manual "Check for
  updates" button (an explicit user action, exempted by established
  precedent), this one fires without any click. Fixed: new exported
  `updates.Enabled()`, checked before the network call.
- **N3** — no test proved the two new routes are actually exempt from the
  real `auth.Middleware` (every existing test drove the bare mux). Added
  two tests mirroring `TestSetupLanguageInstallExemptFromAuthMiddleware`'s
  precedent, one per route.
- **N4** (test-quality note, not fixed as a separate change) — accepted;
  the real non-blocking coverage comes from the template-wiring test.
- **N5** — README: nothing it claims went stale. No action.

## Independent review — round 2, scoped to the fix (Opus, worktree-isolated)

Earned by B1 being a security-class finding. Verified each of B1/N1/N2/N3's
fixes by mutation: for B1 and N3, removed the fix and confirmed the exact
new test fails for the stated reason, then restored; for N2, moved the
guard to fire *after* the network call and confirmed the "never called"
assertion catches it; for N1, confirmed the generated JS parses safely for
all four locales and that `json.Marshal`-encoding neutralizes a
`</script>`-breakout attempt.

One gap found: N1's fix had no direct test (the existing apply-success test
only asserted a non-empty body). Closed in the same session — added
`TestSetupUpdateRestartingHTML_HasRecoveryAffordance`, TDD'd for real
(reverted `setupUpdateRestartingHTML` to its pre-N1 body, confirmed the new
test fails naming exactly what's missing, restored, confirmed green).

Full regression gate re-run clean after both rounds: `gofmt -l .` empty,
`go build ./...`, `go vet ./...`, full `go test ./...` (no failures),
`guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-compliance-claims.sh`, `guard-webkit-version.sh`,
`guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
`guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
`guard-autofill-suppression.sh`, `check-brand-assets.sh`,
`guard-makefile-version.sh` — all pass.

## Verified beyond automated tests

Drove the real compiled binary against a fresh first-boot SQLite DB (temp
`UT_DATA_DIR`), pointed `internal/updates`' release-check URL at a local
stub server returning a fake newer tag (reverted immediately after — never
committed), and screenshotted step 1 in `en`, `fa` (RTL), and `ar` (RTL)
with a real headless Chromium. Banner rendered cleanly in all three: no
overlap/cut-off/wrapping, correct RTL mirroring (dot progress indicator,
button, text all correctly mirrored), button full-width and legible. Did
not visually check the "available but unsupported" fallback (this
sandbox's temp install happened to be writable, so `selfupdate.Supported()`
returned true) — that branch is covered by handler-level tests instead
(reuses the already-shipped, already-screenshotted `updateUnavailableHTML`
from the Settings page). Did not click "Update now" for a real download —
covered by handler tests with faked seams instead, to avoid a live
download/re-exec against a fake release tag in this environment.

## Explicitly deferred / accepted

- `/api/setup/update-apply` is directly POSTable during the first-boot
  window without ever having seen the banner (it's auth-exempt by design,
  same trust tier as the existing `/api/setup/language` plugin-install
  route) — accepted, matches established precedent, not a new hole.
- Machine-translated `ar`/`fa`/`tr` strings (NAS Ollama endpoint
  unreachable from this cloud session, per `reference/translation.md`) —
  follow-up card filed for a local/interactive session to re-verify against
  the documented pipeline, same established pattern as ut-docs#915/#941/#982/#991.

## Safe-to-merge verdict

Yes. Both review rounds confirm no blocking issues remain; full gate green.
