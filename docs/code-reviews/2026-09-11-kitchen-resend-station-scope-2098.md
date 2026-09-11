# Code review: scope kitchen ticket resend to the requesting station (ut-docs#2098)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#2098
**Branch:** `fix/2098-kitchen-resend-station-scope`
**Complexity:** medium (Dev: Sonnet inline, Review: Opus subagent, fresh context)

## What shipped

`KitchenPrintFailed`/`SetKitchenPrintFailed` is a single flag per
`receipt_no` (`internal/data/order_status_repo.go`), and `POST
/api/print/kitchen` always resent **every** station's ticket for that
sale — it had no way to target just one failed station. This was
pre-existing behavior, but ut-docs#2063 (merged same day) newly put a
resend trigger in front of kitchen staff on the per-station
`/kitchen-display/{station_id}` board (`orders_list.html` is shared
verbatim between `/orders` and `/kitchen-display/{station}`), so a
station worker fixing their own failed ticket would also silently
re-print every *other* already-succeeded station's ticket — a real
duplicate-ticket risk on any multi-station shop (grill + bar).

Fix: `buildKitchenTargets`/`printKitchen` take an optional `stationID`
filter (`internal/pages/kitchen_print.go`). `""` keeps the original
every-destination behavior (the shop-wide `/orders` board,
`order_status.go`, and the automatic post-sale print,
`printKitchenAsync`); a real station id scopes the resend to that one
station's own ticket and excludes the default/unrouted bucket
entirely. `orders_list.html` now carries the fragment's own
`.StationID` into the resend button's `hx-vals`, set to the real
station id by `kitchen_display.go` and to `""` by `order_status.go`.

## Independent review (Opus subagent, different model from the Sonnet implementation)

**Verdict: core filter logic sound; 3 real findings in the HTTP
handler's flag/response semantics, all fixed before this commit — not
a second review round on the whole diff, just this scope (see
"Model routing by complexity" — a second round is earned by a
blocker-class finding and stays scoped to the fix).**

The reviewer ran real code (wrote and ran a temporary reproduction test
in `internal/pages/`, then deleted it) rather than reading alone. Findings:

1. **BLOCKER — a display-only station's board resends nothing, reports
   "✓ sent", and clears the ⚠ warning.** `kitchenDisplayStation` admits
   any station with `ShowsOnDisplay()` (`display` or `both`), but only
   `printer`/`both` ever produce a target. Under a station filter, a
   display-only station's own board resolves to **zero targets** —
   `printKitchen` returned `total=0, failures=nil, err=nil`, which the
   handler's `ok := err == nil && len(failures) == 0` read as success:
   it rendered `✓ Kitchen ticket sent` and cleared
   `kitchen_print_failed_at`, even though nothing was ever printed and
   the real failure was still real. Verified live: repro printed
   `status=200 body="<span>✓ Kitchen ticket sent</span>"` and
   `kitchen_print_failed_at=""` after the call.
2. **BLOCKER/should-fix — a station-scoped success clears the
   SALE-wide ⚠, hiding another station's still-failed ticket.**
   `kitchen_print_failed` is a column on `sales` (per-sale, not
   per-station). Pre-fix, `ok` meant "every destination for this sale
   printed" — correct to clear on. Post-fix, `ok` could mean "the one
   station I scoped to printed" while a different station's ticket was
   still unsent. Verified live: a Grill-scoped resend that succeeded
   cleared the flag while Bar (routed in the same sale, its own address
   unopenable) never printed at all.
3. **Should-fix — an unknown/stale `station_id` is a silent no-op that
   also clears the warning.** `station_id` was read from the form and
   never validated; the non-malicious path is real (a kitchen-display
   board left open after an admin disables/re-creates a station).
   Session-auth + same-origin means this isn't a security hole, but the
   dishonest-flag outcome (worse than "0 tickets sent, reported as
   success" — it also erases the only UI signal the ticket is missing)
   was worth fixing regardless.

Verified clean by the reviewer (re-derived from code, not just
re-stated): the `routed`-flag hoist is behavior-preserving for
`stationID == ""` (every pre-existing test path byte-identical); a line
routed to two stations under a filter correctly lands on only the
filtered station's ticket, never leaks to the other station or the
default bucket (`TestPrintKitchen_StationFilterOnlyPrintsThatStation`
exercises the actual bug, not a pre-existing pass); the
`{{ $stationID := .StationID }}` template capture is correctly scoped
outside `{{ range .Orders }}`, not shadowed; `jsonVals` serializes an
empty `station_id` safely; no other caller of
`buildKitchenTargets`/`printKitchen`/`orders_list.html`'s resend button
was missed (grepped the whole repo).

## The fix for all three findings

`registerKitchenPrintAPI`'s handler (`kitchen_print.go`) now treats
`kitchen_print_failed` as strictly per-sale, never assumed cleared or
failed by a partial, station-scoped view of it:

- `total == 0` (nothing routed to this call at all — a display-only
  board, or a station id that resolves to nothing for this sale) is
  **not evidence either way**: the flag is left exactly as found, and
  the response is a distinct new message (`kitchen.print.
  station_nothing_to_send`, added to `en`/`ar`/`fa`/`tr` — never the ✓
  span) rather than a false success.
- A station-scoped **success** (`stationID != ""`) never clears the
  flag — only an unfiltered call (the shop-wide `/orders` board, or the
  automatic post-sale print) may clear it clean, since only those speak
  for the *whole* sale.
- A station-scoped **failure** still sets the flag, exactly as before —
  that's always true evidence of a real problem regardless of scope.

Three new tests in `print_api_test.go` pin each finding against the
real HTTP handler (not just the lower-level `printKitchen` function),
mirroring the reviewer's own verified repro setups:
`TestManualKitchenReprint_StationScopedNothingToSend_DoesNotFalselyClear`,
`TestManualKitchenReprint_UnknownStationID_DoesNotFalselyClear`,
`TestManualKitchenReprint_StationScopedSuccess_DoesNotClearSaleWideFlag`.

Also addressed the reviewer's nit: the `routed = true` inline comment in
`buildKitchenTargets` overstated what the hoisted flag does on its own
(the default bucket is already unconditionally excluded under any
filter by `stationID == ""`, independent of `routed`) — reworded to
call it defense-in-depth rather than load-bearing.

**Not applicable / no action needed:** the reviewer's remaining nit
(the filter test using `printer`-only stations rather than `both`, so
it alone wouldn't have surfaced finding #1) is now moot — finding #1
has its own dedicated handler-level test using a real
`display`-destination station.

## Verification

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean.
- `golangci-lint run ./...` — 0 issues.
- `go test ./...` — all packages green (including the 3 new
  finding-pinning tests, and every pre-existing kitchen/orders test
  unchanged in outcome).
- CI-blocking guards re-run: `guard-data-access.sh`,
  `guard-i18n.sh` (new `kitchen.print.station_nothing_to_send` key
  resolves in all 4 shipped locales), `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` (the `kitchen-stations`
  help-topic bullet-count baseline entries for ar/fa/tr updated
  8→9 to track the new documented bullet — pre-existing ut-docs#1962
  `bold_leadins` drift on the same entries left untouched),
  `guard-docs-shots.sh` (screenshots regenerated via `make docs-shots`,
  reusing this session's pre-installed Chromium — the shared-partial
  and page-handler edits invalidated the manual's single combined
  surface hash across all 30 topics, not just kitchen-stations; the
  other topics' screenshot diffs reflect `main`'s current state, not a
  behavior change from this card), `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`,
  `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
  `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh` — all green.
- `guard-shellcheck-version.sh` fails in this cloud sandbox
  (`shellcheck` binary not installed here) — a pre-existing environment
  gap, not caused by this diff, which touches no `.sh` files; expected
  to run normally on the real `ubuntu-latest` CI runner.

## Scope

Manual updated in the same branch: `web/help/{en,de,ar,fa,tr}/
kitchen-stations.md` gain a bullet explaining the per-station scoping
(translated into all four non-English shipped locales, not deferred).
No ADR needed — a bug fix to existing, already-decided behavior
(ut-docs#516/#544's station routing), not a new architectural decision.
No money/tax/plugin-verification surface touched.
