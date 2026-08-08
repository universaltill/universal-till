# Code review: CI guard against self-order kiosk handlers reaching `d.Engine` (ut-docs#450)

**Date:** 2026-08-08
**Card:** [ut-docs#450](https://github.com/universaltill/ut-docs/issues/450)
**Complexity:** easy
**Author (Dev):** inline (scrum-master session, Sonnet)
**Reviewer:** independent fresh-context Sonnet subagent (round 1), a second
scoped-to-the-fix Sonnet subagent (round 2)

## What shipped

`ut-docs#449` split the self-order kiosk's basket (`common.Deps.KioskEngine`)
from the cashier's (`common.Deps.Engine`) because `/self-order` and
`/api/self-order/*` are auth-exempt — reachable by any anonymous LAN client
— and a handler that reaches `Engine` instead of `KioskEngine` reads or
mutates the cashier's live sale. That split was enforced by developer
discipline only.

Adds `scripts/ci/guard-kiosk-engine.sh`: a grep-based CI guard (same spirit
as the existing `guard-data-access.sh`) that fails if any file registering a
`/self-order` or `/api/self-order/*` route references the cashier's `Engine`
field as code. Comment-only mentions are exempt (existing prose like
"KioskEngine, not Engine (ut-docs#449)" doesn't trip it); a deliberate,
reviewed exception needs an inline `// kiosk-engine-guard:allow <reason>`
comment. Wired into `.github/workflows/ci.yml` next to the data-access
guard, with a regression test (`guard-kiosk-engine_test.sh`) following the
same fixture-plant-and-clean pattern as `guard-data-access_test.sh`.
Documented in `universal-till/CLAUDE.md`'s guard list.

## Independent review — round 1 (blocker-class findings)

A fresh-context Sonnet subagent, given no prior reasoning, read the real
kiosk-route files for context and adversarially tested the first draft.
Verified two real bypasses, both realistic (each mirrors a coding
convention already live elsewhere in this exact package):

1. **Bare-path route registration missed.** The route-detection regex
   required the Go 1.22 `"METHOD /path"` `HandleFunc` literal shape. It
   missed the equally common bare-path style (`mux.HandleFunc("/api/pos/scan",
   ...)`) already used in `pos_api.go` and `catalog/handlers.go` — a kiosk
   route registered that way, reaching `d.Engine`, sailed through the guard.
2. **Hardcoded receiver name.** The `Engine`-reference check only matched
   the literal identifier `d`. Two other page-registration functions in this
   same package already use different receiver names for the identical
   `*common.Deps` parameter (`registerShiftsAPI`/`registerInventoryAPI` use
   `dp`, `registerPluginStore` uses `deps`) — a kiosk handler written with
   one of those names, reaching `d.Engine`'s equivalent, also sailed
   through.

Both were verified by planting an adversarial fixture, confirming the guard
passed it (a false negative), then deleting the fixture. This is a
security-relevant backstop for kiosk/cashier basket isolation on an
anonymous surface, so per this pipeline's process-depth rule a
blocker-class finding on a review earns a second round, scoped to the fix.

## Fix

- Route-detection regex: `([A-Z]+ )?` before the path makes the HTTP-method
  token optional.
- Engine-reference regex: `[A-Za-z_][A-Za-z0-9_]*\.Engine([^A-Za-z0-9_]|$)`
  matches any receiver identifier, not just `d`. Still cannot match
  `.KioskEngine` — the pattern requires a literal `.` immediately before
  `Engine`, and there is no `.` directly preceding `Engine` in
  `KioskEngine`.
- Added two regression cases reproducing both verified bypasses
  (`DirectEngineAccessBarePath`, `DirectEngineAccessAltReceiver`) so a
  future regression in either dimension fails CI, not just a live review.
- Corrected the guard script's own header comment and the `CLAUDE.md` entry,
  which had overclaimed "the receiver every page-registration function uses"
  — factually wrong given the `dp`/`deps` counterexamples already in the
  codebase.

## Independent review — round 2 (scoped to the fix)

A second fresh-context Sonnet subagent verified, narrowly:

- Both round-1 findings **CLOSED** — replanted both original adversarial
  fixtures (bare-path route, alternate receiver) against the fixed guard;
  both correctly rejected. Also tried a receiver name never used anywhere
  in the package (`x.Engine`) and a combined bare-path + non-`d`-receiver
  fixture — both caught, confirming the fix is receiver-agnostic in
  general, not just patched for the two known counterexamples.
- **No new false-positive surface.** Ran the loosened regex directly
  against the whole `internal/pages` tree (~110 matches) and hand-checked
  every result: all are legitimate `Engine` references in non-kiosk files
  (already outside the guard's file-scoping) or `KioskEngine` references
  that structurally cannot match. Zero accidental matches.
- Guard and its regression test both pass cleanly on the real, unmodified
  codebase; `go build ./...` / `go vet ./...` clean; working tree confirmed
  clean of fixture artifacts throughout.

**Verdict: ship.**

## Verified beyond the guard's own regression test

- `go build ./...`, `go vet ./...`, `go test ./...` all clean (full suite,
  run once after the fix — see this pipeline's gate-once rule).
- `bash scripts/ci/guard-data-access.sh` (the sibling guard, to confirm no
  interference) still passes.
- Manual re-read of `internal/pages/self_order_page.go` and
  `self_order_shop.go` confirms both already use `KioskEngine` exclusively
  in every handler body — the code this guard protects is currently
  compliant, so the guard should stay green on `main` and only fire on a
  genuine future regression.
- No help-manual or i18n surface touched — this is a CI-only, developer-facing
  change with no shop-owner-visible behavior, so no `web/help/` topic update
  applies.

## Files changed

- `scripts/ci/guard-kiosk-engine.sh` (new)
- `scripts/ci/guard-kiosk-engine_test.sh` (new)
- `.github/workflows/ci.yml` — two new steps, next to the data-access guard pair
- `CLAUDE.md` — new "Self-order kiosk isolation" section + `Before committing` update
