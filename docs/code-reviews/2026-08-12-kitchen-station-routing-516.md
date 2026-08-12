# Code review: kitchen station routing — printer slice

**Card:** universaltill/ut-docs#516
**Date:** 2026-08-12
**Complexity:** hard — Dev via a Fable subagent, Review via an independent
Opus subagent (fresh context). One review round; the round found several
real, non-blocking issues (no money/tax/data-loss/security-class blocker),
all fixed in this same diff rather than earning a second round.

## What shipped

Replaces the single hardcoded kitchen-ticket target (`const kitchenStation
= "KITCHEN"`, one printer address) with a configurable station/routing
model, scoped to the printer-routing half of #516 — the KDS on-screen
display destination is explicitly deferred to #544 (already noted on the
issue by a prior Architect pass), as is #517 (live order view) and #526
(order status, landed separately on `main`).

- `internal/db/migrations/034_kitchen_stations.sql`: `kitchen_stations`
  (name, `destination_type` admitting a future `'display'` value,
  `printer_address`, soft `enabled`), `item_station_routes` and
  `category_station_routes` (many-to-many, `ON DELETE CASCADE`).
- `internal/data/kitchen_stations_repo.go`: station CRUD + routing
  replace-all + `ResolveKitchenStations`, the single SQL-backed source of
  the routing algorithm — item-level routes override category-level (no
  union), enabled-only, tier decided by row existence before the enabled
  filter runs, unrouted/all-disabled resolves to an absent key.
- `internal/pages/kitchen_print.go`: `buildKitchenTargets` groups a sale's
  lines by resolved station (or one shared default bucket for unrouted
  lines); `printKitchen` sends every target **concurrently**, each on its
  own fresh timeout, so one dead printer can never block, slow down, or
  starve another's timeout budget — offline-first, and the ticket header
  is deterministic (station name sort). Zero-stations-configured output is
  pinned byte-identical to the pre-#516 single ticket.
- `internal/pages/kitchen_stations_page.go` + `web/ui/pages/kitchen_stations.html`:
  new manager-gated `/kitchen-stations` settings page (station CRUD,
  category×station routing matrix, item-override search) — modeled
  closely on `internal/pages/locations_page.go`.
- i18n keys + help topic in all four locales; `printing.md` cross-references
  the new topic in all four locales; `ut-docs/architecture/receipt-printing.md`'s
  scope note updated to point at where routing actually landed.

## Independent review (Opus, fresh context)

Read every changed/new file, re-ran `go build`, `go vet`, the affected test
packages and all 5 guard scripts independently (all green), and personally
verified rather than trusted: replicated `ResolveKitchenStations` against a
scratch DB across 6 edge cases (all correct); confirmed the two
string-interpolated-table-name call sites in `replaceStationRoutes` are
compile-time literals only, not an injection vector; traced the
`printKitchenAsync` → `printKitchen` → `sendKitchenTicket` path end-to-end
to confirm a station failure can never block the sale; reproduced the Go
1.22+ `ServeMux` pattern conflict Dev's routing-path deviation was based on
(genuine — `/api/kitchen-stations/categories/{id}` really does conflict
with `/api/kitchen-stations/{id}/active`); spot-checked all 4 locales'
`kitchenstations.*` translations as genuine, not placeholder/copy-paste.

**No blockers.** Five real-but-non-blocking findings, all fixed here:

- **R1 — `item_station_routes`/`category_station_routes` had no `ON DELETE
  CASCADE`.** `POSRepo.CleanupObsoleteItems` hard-deletes inactive,
  never-sold items and only pre-deletes children that lack a cascade
  (`inventory`, `price_history`) — a station-routed obsolete item would
  have 500'd the entire cleanup batch with a raw FK error. Fixed by adding
  `ON DELETE CASCADE` to both FKs in 034 while the migration is still
  unmerged (append-only after release makes this an expensive fix later).
  Regression: `TestCleanupObsoleteItems_ItemWithKitchenStationRouteCascades`
  (`internal/data/reset_test.go`) — confirmed it reproduces the FK failure
  against the pre-fix schema before adding the cascade.
- **R2 — a station with a blank printer address silently swallowed its
  lines.** `TransportForAddress("")` returns `(nil, nil)`, which
  `sendKitchenTicket` reads as "no printer configured" — those lines
  weren't rerouted to the default bucket, they vanished; in the
  legacy-setting-unset case, the whole sale printed nothing with **no
  audit row**. Fixed two ways: the settings page now requires a non-empty
  address on create/update (new key
  `kitchenstations.error.address_required`, all 4 locales), and
  `buildKitchenTargets` treats a blank-address station as unroutable
  (falls to the default bucket) as defense-in-depth against any
  pre-existing/otherwise-blank data. Regression:
  `TestPrintKitchen_BlankAddressStationFallsBackToDefault`.
- **R3 — the 15s send budget was shared across serial sends, so "one dead
  printer never blocks the others" only held for ≤2 dead targets**, and
  the per-failure audit write used that same (possibly-expired) context,
  so a starved failure's own audit row could silently not get written.
  Fixed by sending every target concurrently, each on its own fresh
  `context.WithTimeout(context.Background(), 15s)`, with failure audits on
  a separate fresh 5s background context — `sync.WaitGroup` +
  mutex-guarded failures slice. The existing
  `TestPrintKitchen_OneTargetFailureDoesNotBlockOthers` still covers the
  ≤2-target case; the fix generalizes it to any number of dead targets
  (not independently re-pinned at N≥3 — logically covered by the
  independent-context change, considered acceptable rather than adding a
  slow multi-target-timeout test).
- **R4 — deferred, not fixed here.** The `destination_type` `CHECK`
  constraint permits exactly `'printer'` or `'display'`, not "both" — the
  card's own text says a station can be printer, display, **or both**.
  Genuinely needs #544's own design (a `'both'` station's printer-path
  handling isn't just a schema tweak, it's routing logic #544 owns) rather
  than a rushed schema change here; noted on ut-docs#544 so it's designed
  in from the start instead of discovered as a second append-only rework.
- **R5 — same-session doc obligations.** `web/help/en/printing.md` (+
  ar/fa/tr) didn't point a reader at the new Kitchen stations topic; fixed
  with one cross-reference line per locale. `ut-docs/architecture/receipt-printing.md`'s
  "Out of scope" list still named "kitchen printers/KDS routing (G3)"
  wholesale; replaced with a note that routing landed (this card) and only
  the KDS display half (#544) remains out of scope.

Nits noted, not fixed (none change behavior, none block merge):

- `TestPrintKitchen_ZeroStations_ByteIdenticalLegacyTicket` compares
  against the same shared `kitchenTicketFor`/`kitchenItemsFor` helpers the
  station path now also uses, so it pins "the two paths agree" more than
  "matches the literal pre-#516 bytes" — well-mitigated by the pre-existing
  `internal/print/kitchen_test.go` golden-byte coverage of the render
  itself.
- `TestKitchenStationsPagePermissions` doesn't individually assert 403 on
  every mutating route (`/{id}`, `/{id}/active`,
  `/routes/items/{itemID}`) — each is gated in code identically to the
  ones that are asserted (`requireManager` as the first statement); not
  independently pinned.
- The category×station matrix renders disabled stations as ordinary,
  tickable columns with no visual "(inactive)" marker — ticking one saves
  a route that does nothing until reactivated. Minor UX polish, filed as
  a new Backlog card rather than rushed into this diff.
- `web/ui/pages/settings.html`'s pre-existing printer/kitchen-address
  fields have the same RTL-corruption class this review caught and fixed
  in `kitchen_stations.html` (missing `dir="ltr"` on address inputs) — not
  introduced by this diff, filed as its own Backlog card for a repo-wide
  sweep.

## Verified beyond the automated suite (this session, driving the real app)

- Built the binary, ran the real first-boot setup wizard through a real
  Chromium (Playwright, global install), created a manager PIN, logged in
  for real — not `UT_AUTH=off` (this page's `requireManager` gate has no
  auth-disabled bypass, matching `locations_page.go`'s precedent, so a real
  session was the only way to reach it).
- Drove `/kitchen-stations` for real: created a station, watched the
  category routing matrix render actual demo-catalog categories
  (Cleaning/Food/Dairy/Drinks/…), no console errors.
- **Caught a real defect via the required RTL check** (`?lang=fa`): the
  `printer_address` value rendered corrupted/right-truncated inside its
  edit `<input>` (RTL bidi reversal on a technical LTR string in a narrow
  fixed-width box) — fixed with `dir="ltr"` on the three address-bearing
  elements, re-screenshotted to confirm the address now reads correctly
  end-to-end in the RTL layout.
- Visual-check attestation: light theme at 1280×900 (empty state + after
  creating a station, full page) — looked correct, labels above fields,
  nothing overlapping/cut off. `fa`/RTL at the same viewport, same states —
  confirmed correct after the `dir="ltr"` fix. **Not** independently
  re-verified: dark theme (this app's theme is a server-side setting, not
  `prefers-color-scheme` — toggling it wasn't in scope for this pass; the
  page uses the same shared card/table/form CSS classes as
  already-dark-theme-verified pages like `locations.html`, so it isn't
  believed to diverge, but this wasn't independently screenshotted here);
  the item-override search flow's visual layout (exercised functionally —
  created a station, would need a populated catalog search hit to
  screenshot, not done this pass); mobile/10-inch kiosk viewport sizing
  (this is a manager-only back-office settings page, not a kiosk/sale-screen
  surface, so out of scope per this repo's own layout-test precedent).
- `go build ./...`, `go vet ./...`, full `go test ./... -race -count=1`
  (every package green, run twice — once before the review fixes, once
  after) and all 5 guard scripts (`guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`) — green both times.
- No real client/shop name anywhere in the diff or in this manual-testing
  session (seeded "E2E Kitchen Test Shop"/"E2E Test Shop" only, both
  discarded with their throwaway temp-dir DBs); no secret-shaped literal.

## Safe-to-merge verdict

Yes. R1–R3 and R5 are fixed in this diff; R4 is intentionally deferred to
#544 where it belongs; the remaining nits are non-blocking and either
filed as follow-up Backlog cards or accepted as-is.
