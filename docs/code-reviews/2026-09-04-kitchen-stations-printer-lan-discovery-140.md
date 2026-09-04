# Code review: printer LAN discovery for Kitchen Stations (ut-docs#140)

**Date:** 2026-09-04
**Scope:** `internal/discovery/browse.go`, `internal/discovery/browse_test.go`,
`internal/pages/kitchen_stations_page.go`, `internal/pages/kitchen_stations_page_test.go`,
`web/ui/pages/kitchen_stations.html`, `web/locales/{en,ar,fa,tr}.json`,
`web/help/{en,ar,fa,tr}/kitchen-stations.md` + regenerated screenshots,
`README.md`.
**Card:** ut-docs#140 "Zero-config LAN auto-discovery for printers, payment
terminals, displays and tills" — scoped down at BA time (documented on the
issue) to the printer half only for this cycle.

## What shipped

Extends `internal/discovery` — previously scoped to till-to-till mDNS
pairing (ADR-0033) only — to also browse for network printers over the
Bonjour/mDNS service type `_pdl-datastream._tcp` (raw AppSocket/JetDirect,
port 9100), the exact wire shape `internal/print.TransportForAddress`'s
"network" mode already speaks. `internal/discovery.Browse`'s internals were
generalized into `scan[T]`/`scanOnce[T]` so till discovery and the new
`BrowsePrinters` share one IPv6-retry/partial-failure/goroutine-cleanup
implementation instead of two.

A new manager-gated `GET /api/kitchen-stations/discover-printers` endpoint
(mirroring `discovery_api.go`'s existing till-discovery handler) and a
"Find printers on this network" button on the Kitchen Stations admin page
offer LAN candidates that fill the new-station form's address field on
click — nothing is auto-trusted or auto-wired; the operator still reviews
and submits the form themselves.

Deliberately **not** discovering `_ipp._tcp`: this codebase has no IPP
(HTTP-based) print transport, so discovering an IPP-only printer would
offer a device nothing here could actually print to. Documented as a
follow-up (ut-docs#1527) rather than built speculatively.

## Independent review

Spawned an Opus subagent (complexity:medium → Opus review per
`scrum-master`'s model-routing table), isolated in its own git worktree,
with no visibility into the implementation reasoning. It ran the full
gate itself (`gofmt`, `go build`, `go vet`, `go test ./...`, all relevant
CI guards) and did a byte-level comparison of the `scan[T]`/`scanOnce[T]`
generic refactor against the original till-only `Browse`/`browseOnce` to
confirm till discovery's real behavior is unchanged — the riskiest part of
this diff, since ADR-0033's pairing flow depends on it. It also did a live
TDD spot-check: broke `printerCandidateFromEntry`'s TXT-parsing loop,
confirmed the relevant tests fail with the right message, restored it.

**Verdict: PASS, no blocking issues.** Findings, and what was done with
each:

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | `printerCandidateFromEntry` defaulted an unnamed printer's `Name` to the hardcoded English literal `"Printer"` — a Latin-script leak into ar/fa/tr operator views, invisible to `guard-i18n.sh` (it never touches an HTTP response body). | Minor | **Fixed.** `Name` is left empty in Go; the page's JS supplies a localized fallback (`kitchenstations.discover.generic_name`, added to all 4 core locales + the German/Spanish packs) when empty. |
| 2 | `README.md` not updated — standing repo rule requires it the same session it goes stale. | Minor | **Fixed.** Kitchen-station-routing and mDNS-discovery bullets now mention printer discovery. |
| 3 | `lang-pack-drift` will go red on push to `main`: 7 new `en.json` keys need the external `ut-plugin-language-{de,es}` packs updated. | Minor | **Fixed.** Opened universaltill/ut-plugin-language-de#141 and universaltill/ut-plugin-language-es#140, both subscribed and watched; their own `key-drift` CI is red only because it fetches core's `en.json` from `main`, which doesn't have these keys until this PR merges — commented on both explaining the sequencing, will re-check once this PR lands. |
| 4 | The "Find printers" button rendered after "Create station" in tab/reading order — discovery logically precedes creation. | Nit | **Fixed.** Reordered inside the form: Name → Address → Find printers (+ results) → Create station. |
| 5 | A 403 from the new endpoint returns an HTML error page, not the `{data,error}` JSON envelope the success path uses — `r.json()` then falls into the page's generic `.catch()` message. | Nit | **Accepted as-is** — matches every other route on this page; not a regression this diff introduces. |
| 6–9 | No per-field length cap on LAN-supplied printer name (real impact small, capped at 64 candidates); IPv6 link-local fallback has no zone id (till discovery has the same shape); no dedup across interfaces (same as till discovery); a couple of acceptable test-coverage gaps. | Nit | **Accepted** — pre-existing shape this change inherits, or genuinely low-risk; not worth widening scope for. |

## Verified beyond automated tests

- `go test ./...` — full suite green.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally and passing, including `guard-docs-shots.sh` (screenshots
  regenerated via `make docs-shots`, diff scoped to just the
  `kitchen-stations` topic's 4 locale PNGs + `manifest.json` — the other
  screenshots the full suite re-captures each run are date-sensitive
  content noise, not affected by this diff, and were reverted to keep the
  PR scoped).
- Manually opened `web/help/img/en/kitchen-stations.png` and
  `.../ar/kitchen-stations.png` and visually confirmed the new button
  renders correctly (including RTL layout).
- `internal/discovery`'s existing till-discovery test suite (17 tests
  covering IPv6 retry, cancellation, goroutine-leak avoidance, candidate
  capping) passes unchanged against the generic-refactored code.

## Deferred (new Backlog cards)

Per "What one item is allowed to cost," #140's own acceptance criteria
were broader than one cycle — split into:
- ut-docs#1524 — kitchen display (KDS) discovery, blocked on #544's
  display-destination UI.
- ut-docs#1525 — re-discovery on DHCP address change.
- ut-docs#1526 — unified "Devices" candidate-list UI shared with #545/#76.
- ut-docs#1527 — IPP-only printers are invisible to this discovery slice
  (documented scoping decision, not a bug).

## Safe to merge

Yes — independent review passed, full local gate green, manual/blocking
findings all fixed in-branch.
