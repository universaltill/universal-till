# Code review: Kitchen Display System, HDMI-local slice (ut-docs#544)

**Date:** 2026-09-05
**Scope:** `internal/db/migrations/{001_init.sql,003_kitchen_station_display_flag.sql}`,
`internal/data/kitchen_stations_repo.go`, `internal/pages/{kitchen_print.go,kitchen_stations_page.go,order_status.go,kitchen_display.go}`,
`web/ui/pages/{kitchen_stations.html,kitchen_display.html}`, `web/ui/partials/orders_list.html`,
`web/locales/{en,ar,fa,tr}.json`, `web/help/{en,ar,fa,tr}/kitchen-stations.md` + screenshots, `README.md`.
**Card:** universaltill/ut-docs#544 — "on-screen station destinations", scoped at BA time to the
**HDMI-local case only** ("a second output on a machine already running the till — no network
hop, no pairing"); the LAN-paired remote KDS device is a separate follow-up.
**PR:** universaltill/universal-till#792

## What shipped

A kitchen station's destination can now be `display` or `both` (prints AND shows on a screen), not
only `printer`. A new per-station live order board at `GET /kitchen-display/{station_id}` (+
`GET /ui/kitchen-display/{station_id}` fragment) reuses the existing `/orders` board's mechanism
end to end — the same `ui/partials/orders_list.html` partial, the same `/api/orders/stream` SSE
push, the same `POST /api/orders/{receipt_no}/status` one-tap endpoint — rather than building a
second live-update mechanism. A new repository method, `ListRecentOrdersForStation`, is the
station-scoped twin of `ListRecentOrders`, implementing `ResolveKitchenStations`' item-overrides-
category routing precedence as a single SQL query. The Kitchen Stations admin page gained a
destination-type selector and a "View display" link for display-capable stations.

## Independent review — two rounds

Spawned an Opus subagent (complexity:hard → Fable builds, Opus reviews, per `scrum-master`'s
model-routing table), isolated in its own git worktree, with no visibility into the implementation
reasoning.

### Round 1 verdict: needs fixes — one blocker, one should-fix

**BLOCKER (fixed).** The original diff widened `destination_type`'s CHECK constraint directly in
`001_init.sql` to add `'both'`. This bricks boot on **every existing install**:
`internal/db/db.go`'s `verifyAppliedMigrations` hard-fails `Open()` the instant an already-applied
migration's on-disk checksum drifts from what `schema_migrations` recorded, and
`idempotentRerunVersions` is empty (version 1 is not allowlisted for re-apply).
`002_refund_of_line_id.sql`'s own header already documents this exact trap and ships an additive
migration for the same reason — this diff did the opposite. The reviewer reproduced the break
empirically (a DB migrated with the prior `001_init.sql`, then reopened with the edited one, fails
`Open()` with an explicit checksum-mismatch error) rather than just asserting it from the doc
comment.

**Fix applied — additive flag, not a widened CHECK.** `001_init.sql` reverted to its exact prior
content (verified byte-identical against `origin/main`). `'both'` is now represented by a plain
additive column: `destination_both INTEGER NOT NULL DEFAULT 0`
(`003_kitchen_station_display_flag.sql`). `destination_type` keeps its original two-value CHECK;
`'both'` is stored as `('printer', 1)` and reconstructed to the public tri-state value everywhere
the repo reads it (`dbDestinationColumns`/`destinationFromColumns` in `kitchen_stations_repo.go`).

A SQLite 12-step table rebuild (the usual way to widen a CHECK) was considered and rejected —
also verified empirically, not just reasoned about: with `foreign_keys=ON` permanently (via
`db.go`'s `_pragma=foreign_keys(1)` DSN parameter) and no way to toggle it mid-transaction (SQLite
treats `PRAGMA foreign_keys` as a no-op inside an active `BEGIN`, and `applyMigration` always wraps
a migration's statements in one transaction), a throwaway probe script rebuilding
`kitchen_stations` inside such a transaction **silently cascade-deleted every
`item_station_routes`/`category_station_routes` row** pointing at the dropped table — even though
`PRAGMA foreign_key_check` reported zero violations immediately before commit. The additive-column
approach sidesteps this whole hazard class: no table rebuild, no DROP, nothing to cascade.

The public Go API (`KitchenStation.DestinationType`, `PrintsTickets()`/`ShowsOnDisplay()`,
`CreateKitchenStation`/`UpdateKitchenStation`'s `destinationType` parameter) is unchanged — only
the physical storage differs — so this needed exactly one test fix:
`TestKitchenStationsPage_DestinationTypeCreateRules` asserted on the raw `destination_type` column
directly and now checks `destination_type='printer'` + `destination_both=1` instead of a literal
`'both'` value, with a comment explaining why.

**Re-verified the fix, both ways:**
- *Unit*: full `go test ./...` green (was already green before the fix, since the blocker only
  manifests on a genuinely pre-existing on-disk database, not a fresh test DB created in-process).
- *Real upgrade path*: built the binary from `origin/main` (`7c11ba1`), ran it against a fresh data
  directory to create a DB with only migration 1 applied (an accurate stand-in for "every existing
  install"), killed it, then started the **fixed** branch's binary against that same DB file. It
  booted clean (`200` on `/`), and `POST /api/kitchen-stations` with `destination_type=both`
  succeeded and rendered "Printer and display" correctly. This is the exact scenario the blocker
  broke, reproduced and shown fixed, not just argued from code.

**SHOULD-FIX (fixed).** The `kitchen-stations` help topic (all four locales) claimed the kitchen
display "shows orders taken on another till once they've synced" — `/kitchen-display` deliberately
has **no cross-till proxy** (unlike `/orders`' `fetchOrdersFromPrimary`), a limitation already
stated correctly in `kitchen_display.go`'s own code comment; the manual just didn't match the code
(the same class of doc/code drift `ut-docs#1350`'s review caught once already). Corrected in
en/ar/fa/tr to state plainly that the screen only shows orders taken on that till, and that a shop
with several linked tills should open the display on the till that actually takes the relevant
orders. `docs-shots`' manifest regenerated (topic-markdown hash changed; no visual change, so no
PNG diff).

### Round 1 — everything else, verified independently and found correct

- **SQL precedence** in `ListRecentOrdersForStation`: item-level routing rows claim the tier by
  *existence* (never falling back to category even when every item-level station is disabled or
  points elsewhere — reasoned through the `NOT EXISTS` structure, then re-verified by TDD below);
  category fallback only when no item-level rows exist; enabled-station-only; `EXISTS` (not
  `JOIN`) gives one row per multi-line order (pinned by
  `TestListRecentOrdersForStation_SplitOrderAppearsOnBothStations`); variant-only (`item_id NULL`)
  lines never match, matching `buildKitchenTargets`' existing limitation. One query, every subquery
  a PK/index-prefix lookup — no N+1.
- **`buildKitchenTargets`/`kitchenPrintingEnabledChecked`**: both switched to `PrintsTickets()`;
  `TestPrintKitchen_ZeroStations_ByteIdenticalLegacyTicket` untouched by the diff and still passes.
- **Repository pattern**: `guard-data-access.sh` — PASS, no SQL outside `internal/data`/`internal/db`.
- **New page**: session-authed like `/orders` (no manager gate — floor work); 404 for
  missing/disabled/printer-only on both the page and fragment route; genuine reuse of the existing
  SSE stream, one-tap endpoint and `orders_list.html` partial (extracted to `orderRowsFor` so both
  boards render the same file); `guard-kiosk-engine.sh` correctly irrelevant (no `/self-order`
  route, no `Engine` reference) — PASS.
- **i18n**: `guard-i18n.sh` PASS; all 10 new keys present and genuinely translated in en/ar/fa/tr.
- **Help + shots**: `guard-help-topics.sh` PASS; `routes:` front matter correctly claims
  `/kitchen-display/{station_id}` via a `{param}` pattern.
- **The two recurring bug classes this pipeline keeps finding** (a file-write handler missing
  `os.MkdirAll`; a cwd-relative path where `paths.Data(...)` belongs): absent — this is a DB+HTTP
  feature, no file writes anywhere in the diff besides `t.TempDir()` in tests.
- **Full gate**: `gofmt -l .` clean, `go build ./...` OK, `go vet ./...` clean, `golangci-lint run
  ./...` 0 issues, `go test ./...` full suite green, `go test ./internal/data/... -race` ok (344s).
- No secrets, tokens, or real client/shop names anywhere in the diff.
- **README**: accurate, not decorative — correctly says "printer, kitchen display screen, or both"
  and lists the same deferred items (LAN-paired device, cross-till proxy, per-line status) the
  code's own limitation comments state.

### TDD re-verification (round 1, done inside the reviewer's own isolated worktree)

Target: `TestListRecentOrdersForStation_ItemRowsClaimTierEvenWhenTheyMissThisStation`. Reverted the
tier-claim guard (deleted the `NOT EXISTS (… ir2 …)` condition, degrading to a naive item-OR-
category union), confirmed two tests then fail with specific, diagnostic messages naming the exact
wrong receipt (`TestListRecentOrdersForStation_ItemRowsClaimTierEvenWhenTheyMissThisStation` and
`TestListRecentOrdersForStation_ItemOverrideWinsOverCategory`), then restored the code and
confirmed `go test ./internal/data/...` is green again. Not false-pass tests.

## Verified beyond automated tests

- Tester's own driven run (real server, real Chromium, real seeded sale, real display station):
  station-scoped filtering correct, RTL/fa layout correct with the station's own name left
  untranslated, one-tap status-to-Collected in a live browser triggers real SSE-driven row removal,
  empty state renders cleanly.
- The migration-safety re-verification above: an actual pre-existing SQLite file, created by the
  prior committed binary, reopened and written to successfully by the fixed binary.
- Both `ut-plugin-language-{de,es}` follow-up PRs (universaltill/ut-plugin-language-de#149,
  universaltill/ut-plugin-language-es#148) carry real, non-machine-identical translations for all
  10 new keys, validated locally against this branch's `en.json`: 1943/1943 keys, 0 drift. Their
  own `key-drift` CI is red only because it fetches core's `en.json` from `main`, which doesn't
  have these keys until this PR merges — will go green once this PR lands (same sequencing as the
  2026-09-04 printer-discovery lang-pack follow-up).

## Deferred (explicitly out of scope, documented in code comments)

- LAN-paired remote KDS device (separate machine, its own pairing/auth/liveness) — a follow-up
  card; `ut-docs#1524` (kitchen-display auto-discovery, filed from the #140 review) already depends
  on this slice.
- Cross-till proxy for `/ui/kitchen-display` — reads this till's local orders only, unlike
  `/ui/orders`'s `fetchOrdersFromPrimary`. Candidate new Backlog card: a station-scoped sync
  endpoint on the primary so a replica's kitchen screen can see the shop-wide board.
- Per-line status — a split order shows on every station it routes to and clears from all on a
  terminal tap (matches the existing per-order granularity everywhere else, not a regression this
  card introduces).

## Safe to merge

Yes — both review rounds' findings fixed and re-verified (the blocker with a real pre-existing-
database boot test, not just a passing unit suite), full gate green, independent Tester pass
already recorded, language packs prepared and sequenced correctly.
