# 2026-09-05: replica write protection for tables & kitchen-stations admin pages

**Card:** [ut-docs#1585](https://github.com/universaltill/ut-docs/issues/1585) — split from #1554.
**PR:** universaltill/universal-till#(opened by this change) — `fix/1585-replica-write-protection-tables-kitchen-stations`.

## What shipped

`tables` and `kitchen_stations`/`category_station_routes`/`item_station_routes`
sync shop-wide as admin tables (`internal/data/sync_admin_repo.go`'s
`adminTables`, one-way primary-wins pull, ut-docs#1546). Neither admin page
(`internal/pages/tables_page.go`, `internal/pages/kitchen_stations_page.go`)
checked whether the till was a replica before writing — an edit made
directly on a joined till would succeed locally, then silently vanish (a
new row deleted, an edit reverted) on the very next admin sync pull, with
no error and no explanation.

Fix follows the already-established, twice-shipped pattern in this repo:
`registers_page.go`'s `requirePrimary` gate (ut-docs#1590) and
`plugins_store_page.go`'s replica check (ut-docs#460) — refuse the
mutation with a clear localized message when `d.SyncPrimaryURL(ctx) != ""`.

- **tables_page.go**: `requirePrimary` gates all four mutating routes
  (`POST /api/tables`, `.../{id}`, `.../{id}/active` via redirect+`err=`;
  `.../{id}/position`, a JS-fetch route, via `409 Conflict` since it has no
  redirect target — `tables.html`'s `persistPosition` now special-cases
  that status to show the specific message instead of the generic
  save-failed one).
- **kitchen_stations_page.go**: `requirePrimary` gates station create,
  active-toggle, category routes and item routes. The station **update**
  route gets a narrower, separate carve-out instead of the blanket gate:
  `printer_address` is deliberately till-local and excluded from sync
  (`skipCols` in `sync_admin_repo.go`) — a joined till is *expected* to set
  its own printer address per station (pinned by the existing
  `TestAdminDumpApplyRoundTrip_KitchenStationPrinterAddressStaysLocal`).
  The update handler now compares the posted `name`/`destination_type`
  against the current DB row: unchanged → allowed, applied via a new
  `SetKitchenStationPrinterAddress` repo method (address-only write);
  changed → refused with the same replica error. Everything else on that
  route (auth, validation, not-found handling) is unchanged.
- New i18n keys `tables.error.replica_use_primary` /
  `kitchenstations.error.replica_use_primary` in all four locales
  (en/ar/fa/tr).
- Help manual (`web/help/{en,ar,fa,tr}/{tables,kitchen-stations}.md`)
  updated with "Good to know" bullets describing the shop-wide-vs-per-till
  split, in the manual's own established vocabulary ("main till"/"joined
  till" — not the internal code's "primary"/"replica"/"satellite" terms).
  Screenshots regenerated (`make docs-shots`); the tables/kitchen-stations
  screens themselves show no pixel diff (the change is text-only and only
  visible on an error path), confirming no unrelated visual change slipped
  in.

## Independent review

Independent **Opus** review (per this card's `complexity:medium` routing —
Sonnet built it, Opus reviewed it), run read-only in an isolated git
worktree against the pre-review commit. It re-derived the diff rather than
trusting the PR description: enumerated every mutating route in both files
against the actual handler bodies (not the comments), traced the
printer_address carve-out's `destination_type` normalization end-to-end
(`stationForm` vs. `GetKitchenStation`'s `destinationFromColumns`) to rule
out a way to sneak a shop-wide change through it, traced `upsertRow`'s
`skipCols` handling to confirm a replica's local `updated_at` bump on the
carve-out path is inert (overwritten by the next sync pull, not a
conflict-resolution signal), and ran the full gate itself
(`gofmt`/`build`/`vet`/`go test ./...`/`golangci-lint`/all the CI-blocking
guards) rather than accepting the numbers reported here.

**TDD re-verification, done for real (not taken on trust):** the reviewer
broke the code three separate ways in its own throwaway worktree, ran the
specific test each time, then restored:

1. Neutralized `tables_page.go`'s `requirePrimary` and the position 409
   check → `TestTablesPage_MutationsRefusedOnReplica` failed with
   `create on replica: code=303 loc="/tables"` (the create silently
   succeeded) — a real, demonstrated bug, not a compile error.
2. Restored, then neutralized only the position gate → failed with
   `position on replica: code=204, want 409` (the write went through).
3. Neutralized `kitchen_stations_page.go`'s carve-out in **both**
   directions — first so a name change on a replica was allowed (failed
   with `update (name change) on replica: code=303 loc="/kitchen-stations"`,
   the rename went through), then forced the comparison to always refuse
   (failed with `address-only update on replica: code=303
   loc="/kitchen-stations?err=kitchenstations.error.replica_use_primary"`,
   confirming the *permissive* half of the carve-out — a joined till
   setting its own printer address — is genuinely pinned too, not just the
   refusing half).

All three restores confirmed clean (`git checkout --`) and the full test
suite green again afterward.

**Findings, all fixed in this branch (none were blocker-class — no second
review round earned per the model-routing rule):**

- **R1** — `kitchen_stations_page.go`'s `requirePrimary` doc comment
  claimed the update route was *also* blocked by it (an earlier, more
  restrictive draft), which was no longer true once the address-only
  carve-out was added — a genuine trap for a future maintainer reading the
  comment as the design record. Reworded to point at the update handler's
  own separate carve-out instead of contradicting it.
- **R2** — the kitchen-stations manual bullet said a joined till "can view
  but not edit" stations, which is the opposite of the shipped, deliberate
  printer-address carve-out — the single most operationally important
  thing on that page for a real multi-till shop (pointing a shared
  station at each till's own physical printer). Rewritten to state the
  carve-out accurately, including the correct fallback (till's own default
  kitchen printer from Settings, not "always prints locally").
- **R3** — the `fa`/`ar`/`tr` manuals were left un-updated, unlike the
  directly-cited sibling commit (`e441736`, ut-docs#1590) which shipped
  its equivalent bullets in all four locales. `guard-help-topics.sh` only
  checks locale *file* completeness, not per-locale prose drift, so this
  would not have been caught by CI. Fixed: added the equivalent bullets to
  all three locales, using each locale's own already-shipped vocabulary
  (`الجهاز الرئيسي`/`جهاز منضم`, `صندوق اصلی`/`صندوق پیوسته`, `ana
  kasa`/`katılmış kasa`) mirrored from `multitill.md`'s registers bullet,
  not machine-translated fresh.
- **R4** — the new English bullets used the internal code's
  "primary"/"satellite" vocabulary, which doesn't appear anywhere else in
  the shop-owner manual (`multitill.md` uses "main till"/"joined till"
  exclusively). Reworded to match.
- **N1** (nitpick, fixed) — `TestTablesPage_MutationsRefusedOnReplica`'s
  update-on-replica case asserted only the redirect, not that the row's
  `label` was actually untouched, unlike its sibling assertions in the
  same test and the kitchen-stations twin. Added the missing `SELECT`.
- **N2** (nitpick, fixed defensively) — the carve-out's `name`/
  `destination_type` comparison relied on both sides already being
  trimmed; added an explicit `strings.TrimSpace` on the DB-read side too,
  so a joined till could never become permanently unable to set its own
  printer address purely over incidental whitespace, however that
  whitespace got there.
- N3/N4/N5 (nitpicks, no change needed) — a body-format difference from
  the cited 409 precedent (both approaches work; the template-rendered
  page's approach is arguably better here), an audit-action-name
  coarseness already consistent with `registers_page.go`, and 1-2 byte PNG
  encoder churn on two unrelated screenshots from the `docs-shots`
  regeneration (no pixel content changed, confirmed by the reviewer).

Translations for the two new locale keys were built by substituting the
already-shipped, correct nouns (dining tables / kitchen stations, per each
locale's own `kitchenstations.title`/`tables.title` translations) into the
identical sentence template already shipped four times over in the same
locale files (`registers.error.replica_use_primary`,
`locations.error.replica_use_primary`, two `plugins.*` keys) — not via the
self-hosted Ollama pipeline (`reference/translation.md`), whose endpoint
(a private LAN address on the homelab NAS) is unreachable from this cloud
session. The reviewer checked this specifically and confirmed the
substitution is safe here: a fixed sentence with only the object noun
swapped, no gender/case agreement triggered in Arabic, Farsi or Turkish by
that position, and the nouns themselves are the ones already used
elsewhere in each file.

## Verified beyond automated tests

- Ran the real app (`UT_AUTH=off`, `sync.primary_url` set directly via the
  `settings` table) and hit `/tables?err=tables.error.replica_use_primary`
  and `/kitchen-stations?err=kitchenstations.error.replica_use_primary`
  for real over HTTP, then screenshotted both pages in English and Arabic
  (RTL) at 1024×700 with the pre-installed Chromium — banner renders
  above the existing content with no overlap/clipping, RTL mirrors
  correctly (nav rail right-aligned, form fields right-aligned, text
  right-to-left), no layout regression in the surrounding page.
- Directly exercised the position-endpoint's client-side branch via
  `page.evaluate` fetch from a real browser context: confirmed the server
  returns `409` for `/api/tables/{id}/position` on a replica (matching
  what `tables.html`'s `persistPosition` checks for).
- Killed the ad hoc server and removed the scratch DB/screenshots
  afterward.
- **Visual-check attestation**: looked at English and Arabic (RTL) at
  1024×700 for both pages' error-banner state. Did **not** separately
  check Farsi/Turkish rendering, dark theme, or the kiosk/10-inch
  viewport for this specific error banner — it reuses the exact same
  `<p class="login-error">` element and CSS class already shipped and
  presumably visually reviewed for every other error key on both pages
  (e.g. `tables.error.required`, `registers.error.replica_use_primary`),
  so the marginal risk of a new visual defect from adding one more string
  through that same unchanged markup is low, but this is a real,
  explicitly-noted gap in what was actually looked at, not a claim of full
  coverage.
- `make docs-shots` run twice (once mid-review, once after the fixes);
  confirmed via the regenerated `manifest.json` and a `git diff --stat`
  that neither `tables.png` nor `kitchen-stations.png` changed in any
  locale — the default/steady-state screens are genuinely unaffected, only
  the manifest's freshness hash needed updating because the markdown
  prose changed.

## Safe to merge

Yes. All CI-blocking guards, `golangci-lint`, and the full `go test ./...`
pass on the final tree. No blocker-class finding at any point.

## Explicitly deferred (new Backlog candidates, not blocking this card)

- Generalizing this same primary-only-write pattern to the other
  shop-wide admin pages `#1585`'s own body flagged as unchecked
  (`catalog_page.go`, `users_page.go`, `locations_page.go`,
  `permission_settings_page.go`) — out of scope for this card, which
  explicitly named only `tables`/`kitchen_stations`.
- `guard-help-topics.sh` has no mechanism to catch a locale's help-topic
  *prose* silently drifting behind `en` (it only checks structural
  completeness) — this cycle's R3 finding was only caught by a human-
  equivalent review read, exactly as the `reviewer` skill's own note says
  it must be. Worth a follow-up card if this class of drift recurs.
- `reference/translation.md` still says the product ships in "English,
  Turkish, Chinese and Persian" — the actual fourth locale is Arabic
  (`ar`), not Chinese, per `web/locales/`. Pre-existing drift, unrelated
  to this card; noticed in passing, not fixed here to avoid scope creep
  on an unrelated doc.
