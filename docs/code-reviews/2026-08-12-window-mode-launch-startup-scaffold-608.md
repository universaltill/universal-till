# Code review: window-mode/launch-startup scaffold + PIN-gated exit-to-OS

**Date:** 2026-08-12
**Card:** ut-docs#608 (scaffold split off epic #549)
**Scope:** `internal/pages/common/{state,deps,window_controller}.go`,
`internal/pages/{init,settings_page,settings_page_test}.go`,
`web/ui/pages/settings.html`, `web/locales/{en,ar,fa,tr}.json`,
`web/help/{en,ar,fa,tr}/display.md`

## What shipped

The shared foundation the sibling OS-specific cards (#609 macOS, #610
Windows, #611 Linux/Pi) plug into: a Settings UI section for window mode
(fullscreen/kiosk/maximized/normal) and launch-on-startup, both persisted
via the established `KeyKioskIdleReset`-style settings KV pattern, and a
role+**live**-PIN-gated "exit to OS window" action (per the product
owner's explicit #549 requirement — a plain manager session is not
enough). No real OS window-manager calls in this card, by design — a
`WindowController` interface + `NoopWindowController` stub is all that
lands here.

## Independent review (Opus subagent — complexity:medium)

Given the full diff, the design intent, the two deviations Dev had
already self-flagged, and told explicitly to run things, live-run the
binary, and mutation-test at least one TDD claim. It did all of that:
build/vet/gofmt clean, full `go test ./...` green, all six relevant
guards green, one TDD claim independently mutation-tested (found a real
gap — see Minor #2 below), and a genuine live-run against a built binary
(`curl` against all three new endpoints, confirmed persistence round-trip
and correct status codes for valid/invalid input on each).

### Major — fixed

**WindowCtl nil-deref.** `d.WindowCtl.ExitToOS()` would panic if `Deps`
never had `WindowCtl` set — true today for `newFullAuthDeps` (the shared
test helper every settings test uses), which predates this field. The
repo has a written precedent for exactly this shape (`Deps.OrderStatus`
— "handlers nil-check it so bare-Deps tests stay valid"), which
`WindowCtl` didn't follow. Verified by the reviewer running a probe test
that panicked. **Fixed**: handler now nil-checks and falls back to
`common.NoopWindowController{}`, matching the `OrderStatus` convention.
New regression test `TestExitToOSEndpoint_NilWindowCtlDoesNotPanic`
added — confirmed to panic without the fix, pass with it (mutation-tested
by me, restoring after).

### Minor — fixed

**Blank-PIN pre-check had no test proving its actual purpose.** The
handler comment claimed the pre-check exists to avoid burning the
device-wide 5-failure lockout budget (mirroring `shifts_api.go`'s fixed
bug), but the existing test only sent one blank attempt — not enough to
distinguish "pre-checked" from "checked normally and happened to 403
anyway". Reviewer mutation-tested this by deleting the pre-check: the
existing test stayed green, proving the gap. **Fixed**: added
`TestExitToOSBlankPINRejectedWithoutBurningLockoutBudget`, mirroring
`shifts_api_test.go`'s own precedent exactly (6 blank attempts — one over
budget — then a correct PIN must still work immediately). Mutation-tested
by me: fails with `429` on the 6th attempt without the pre-check (proving
the lockout would otherwise burn), passes with it restored.

**`auth.ErrLockedOut` not mapped to 429.** Both cited precedents
(`shifts_api.go`) map a lockout error to 429; this handler collapsed
everything to 403, which would tell a manager locked out by *someone
else's* failed keypad attempts that their own PIN is wrong. **Fixed**:
added the same `errors.Is(err, auth.ErrLockedOut)` → 429 mapping.

**Handler comment misdescribed its own auth-off behaviour.** The comment
claimed the PIN check is "checked the same way as shifts_api.go's...
handlers," but those bypass their PIN block entirely under
`UT_AUTH=off`; this handler deliberately does not. Reviewer verified live
(`UT_AUTH=off`, no session, no PIN → still 403). **Fixed**: comment now
states explicitly that the `UT_AUTH=off` bypass is deliberately omitted
and why (the product owner's requirement was for a PIN check that can't
be switched off; the hook is a no-op today so the cost is zero, but the
action can't be exercised under this repo's usual dev/e2e convention
until #609+ makes it do something real and a manager PIN is seeded —
expected, not a bug).

**No success feedback on window-mode Apply / launch-on-startup toggle.**
Both actions persisted correctly but gave the operator no visible
confirmation (only a failure path was wired). **Fixed**: both now show a
"✓ Saved." message on success (new `settings.display.change_saved` i18n
key, all 4 locales), reusing the pattern already established by the
exit-to-os form two blocks below.

### Minor — deferred as a follow-up, not fixed now

**No audit-log entry for exit-to-OS.** Consistent with the rest of
`settings_page.go` (zero audit calls today) and harmless while the hook
is a no-op, but this is a PIN-gated action whose purpose is breaking
kiosk lockdown, and comparable actions elsewhere in the codebase are
audited. Filed as ut-docs#616 rather than expanding this scaffold's
scope — should land with #609 at the latest, once there's a real action
to audit.

### Nits — accepted, not fixed

Help-doc prose reads slightly developer-facing ("scaffolding," "not-yet-
wired") rather than shop-owner-facing, but it's honest about the
controls' current inertness, which is the actual requirement; the
`exit-to-os-form` has no `method`/`action` and relies on JS
`preventDefault`, consistent with this page's existing HTMX-only forms.

### Explicitly confirmed non-issues (reviewer verified, didn't skip)

No file writes (`os.MkdirAll` class N/A), no cwd-relative paths, no
money, no plugin-signing path touched, i18n complete and genuinely
translated across all 4 existing locales with no German added anywhere,
RTL-safe (no literal left/right), design tokens reused (no new hardcoded
colors/spacing), `/api/settings/*` not in the auth-exempt route list (no
anonymous PIN-guessing oracle), `SaveState` read-modify-write pattern
matches the adjacent `kiosk-idle-reset` handler exactly (no partial-state
clobber), no secrets or real client names.

## Verification (self, after fixes)

- `go build ./...`, `go vet ./...`, `gofmt -l` on touched files — clean.
- Both new regression tests mutation-tested by me directly (not just
  taken from the reviewer's report): each fails with the fix reverted,
  in the exact way the finding predicts, and passes restored.
- `go test ./... -count=1` (full suite, once) — all packages green.
- `guard-i18n.sh`, `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-help-topics.sh`, `guard-plugin-menu-read.sh`,
  `guard-docs-shots.sh` (screenshots regenerated after the fixes, since
  they touched `internal/pages/settings_page.go` and `settings.html`
  again) — all green.

## Verdict

**Safe to merge.** One Major and four Minors found by independent
review, all fixed and re-verified (not argued away); one Minor
deliberately deferred as a scoped follow-up card rather than expanding
this scaffold.
