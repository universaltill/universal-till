# Review: registered country on the my. shop profile (ut-docs#4020) — universal-till

**Date:** 2026-10-12 · **Lane:** local-mahshid76 · **Built by:** Sonnet 5.5 · **Reviewed by:** Fable (independent, read-only)

## What shipped (one change across three repos)
The till reports its `store.country` on every check-in; ut-cloud stores it on the device row and returns a read-only `registered_country` on the profile GET; my. shows it above Address (name via `Intl.DisplayNames`, or "not reported yet"). This repo: **internal/pages/cloudsync_wire.go: `remoteCountryReport` adds `country` to the check-in (only a stored two-letter A-Z code; the in-memory "GB" default is never sent; a read error omits it and never blocks the check-in).**

## Findings (reviewer) and what was done
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A till can claim role `primary` and so set the displayed country (same trust as Currency/Theme/Locale) | Accepted: display only, feeds no decision. Comment added at the lead rule in ut-cloud `claims.go` |
| 2 | minor | Unassigned code (e.g. AA) rendered "AA AA" | Fixed: code shown once; test added |
| 3 | minor | ZZ test passed even if countryName broke | Fixed: asserts exact name + code |
| 4 | minor | Lead rule tested only at the api layer | Accepted: sync test covers storing, api test covers the rule; chain covered |
| 5 | info | Architect note's relabel of the address country hint was not done | Dropped on purpose: the new block's own hint explains the difference |

No blockers, no majors. Reviewer verdict: safe to merge.

## Verified beyond the unit tests
- TDD: every layer was red for the right reason before the fix (cloud: `stored country = "" want "DE"`; till: `country "DE" reported as <nil>`; my.: 11 failing before the component).
- Full ut-cloud `go test ./...` (WSL, zig cc): no failures. Till `pages`, `cloudsync`, `enroll`: ok. my.: lint, tsc, 981+ vitest tests ok.
- Driven run of the real demo page, screenshots looked at: 1024x600 and 360px, en/de/fa/ar (fa/ar RTL): label above value, nothing overlaps, no horizontal scroll, RTL order correct.
- **Not verified:** dark theme, a real phone/touch device (the line has no input or touch handler), a live cloud + real till round trip (the check-in wire is covered by tests on both ends only).

## Deferred
None. Tax regime and currency on the profile were dropped from scope by the Architect note (the tax rate is the shop's own setting, not derived from country).
