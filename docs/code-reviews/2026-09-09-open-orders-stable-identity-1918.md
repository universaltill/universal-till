# Code review: open orders — stable identity, dedicated page, naming fix (ut-docs#1918)

**Date:** 2026-09-09
**Card:** ut-docs#1918 — sub-issue (a) of ut-docs#1903 ("Open orders: our
Hold is park-and-swap, not an order you can add to")
**Branch:** `feat/1918-open-orders-stable-identity`
**Diff:** `internal/pos/hold.go`, `internal/pos/service.go`,
`internal/pages/hold_api.go`, `internal/pages/open_orders_page.go` (new),
`internal/pages/menu_page.go`, `internal/pages/order_status.go`,
`internal/data/held_sales_repo.go`, `web/ui/pages/open_orders.html` (new),
`web/locales/{en,ar,fa,tr}.json`, `web/help/{en,ar,de,fa,tr}/*`, tests
throughout.
**Dev:** Fable (per `complexity:hard` model routing)
**Reviewer:** independent (Opus, fresh context, different model from the
Fable implementation, did not write the code)

## What shipped

1. **Stable order identity across park → resume → re-park.** A held order
   used to mint a fresh `hold-<unixnano>` id and re-run its label fallback
   chain (blank → customer name → clock time) on every re-park, so one
   order changed identity and sometimes name every time it was touched, and
   had no history. Now `pos.Service` carries a `HeldOrigin{ID, Label,
   CreatedAt}` set atomically by `RestoreHeld` when a basket is resumed from
   an existing held sale; `POST /api/pos/hold` upserts under that same
   identity (`HeldSalesRepo.Upsert`, `INSERT … ON CONFLICT(id) DO UPDATE`,
   `created_at` preserved) instead of always inserting fresh. A genuinely
   new order (no origin) is unaffected.
2. **New `/open-orders` page** listing every currently-held order (label,
   table, line count, total, age), modeled on the existing
   `tables_page.go`/`kitchen_stations_page.go` list pattern, with a new
   menu tile.
3. **Orders naming collision resolved** — the existing `/orders`
   kitchen/order-status board's display string changes from "Orders" to
   "Order status" (key namespace `orders.*` untouched, minimal blast
   radius); the new page owns "Open orders" under a fresh `open_orders.*`
   key prefix.
4. `#1381` (dine-in/takeaway survives resume, §12 UStG) re-verified through
   the new stable-identity path specifically.

## Independent review — three real findings, all fixed before merge

**1. `Service.Restore` became dead code once `hold_api.go`'s only
production caller switched to `RestoreHeld`.** `deadcode -test=false .`
confirmed it as unreachable and NOT present in
`scripts/ci/deadcode-baseline.txt` — this would have failed
`guard-deadcode-baseline.sh` in CI (the guard itself can't run in this
sandbox: missing GTK/WebKit headers, so it would only have surfaced on the
PR's own CI run, not here). Fixed by removing the wrapper entirely and
migrating all 11 test call sites to `RestoreHeld(snap, HeldOrigin{})`
directly — the baseline is meant to shrink, not grow.

**2. A cashier could never rename an order after its first park — the
label field was silently swallowed.** The hold dialog still shows an
editable "Name this tab" field on every park, including a re-park, but the
first draft reused `origin.Label` unconditionally. Concrete failure:
resume "12:05" (today's clock-fallback label), realise it's actually table
4, type "Table 4", submit → green toast, but the strip and Open orders
page still say "12:05" forever, with no other route to rename it. Fixed:
`hold_api.go`'s re-park branch now honours a non-blank typed label as an
explicit rename, while a **blank** field still keeps the original (the
fallback chain never re-runs from the clock or an attached customer). Both
directions are now covered by tests
(`TestResumeThenRepark_ReusesSameIDLabelAndFirstParkedTime` for the blank
case, new `TestResumeThenRepark_ExplicitRenameOverrides` for the rename
case).

**3. `heldOrigin` survived a basket emptied line-by-line, so an unrelated
next sale could inherit a dead order's identity.** `removeLocked`/
`removeLineLocked` (used by `Remove`/`RemoveLine`, e.g. voiding items one
at a time) cleared lines but not `heldOrigin` — only `resetLocked`
(New sale) and `Tender` did. Concrete failure: resume "Table 4" (its row
already deleted per the resume contract), customer cancels, cashier voids
each line individually instead of tapping New sale, then rings up an
unrelated walk-in and parks it — the walk-in gets upserted under
`hold-X`/"Table 4"/the original `created_at`, so Open orders shows the
walk-in mislabeled as a 90-minute-old table order. Fixed: both functions
now clear `heldOrigin` when the last line is removed, matching
`resetLocked`'s behaviour. New test:
`TestVoidingEveryLine_ClearsHeldOrigin` (covers both the per-key
`RemoveLine` and the SKU-merged `Remove` paths, and confirms origin
survives while at least one line remains).

**Minor, fixed:** `order_status.go`'s hardcoded HTML `<title>` still read
"Orders" after the rename (pre-existing pattern of hardcoded English page
titles repo-wide, but this card's own rename left this one place where the
collision survived) — changed to "Order status" to match.

## TDD verification (reviewer's own re-check, not just reading the diff)

Reverted the origin-preservation branch in `hold_api.go` and the
`s.heldOrigin = origin` line in `RestoreHeld` independently — in both
cases the relevant new tests failed with the expected assertion messages,
and the no-origin-path tests correctly kept passing. Working tree restored
to the fixed state exactly (byte-identical diff) before continuing.

## Verified beyond automated tests

- `gofmt -l .` clean, `go vet ./...` clean, `golangci-lint run ./...` → 0
  issues, `go test ./...` → all packages green (including the two full
  reruns after applying the three fixes above).
- `scripts/ci/guard-i18n.sh` → 1544 template keys resolve, all locales
  match `en.json`, no hardcoded strings.
- `deadcode -test=false .` no longer flags `Service.Restore` (removed).
- UI verified via Go template tests and a real headless-Chromium
  `make docs-shots` run (screens correct at the 1024×600 kiosk floor
  viewport) — **not** on a physical tablet; no device available in this
  cloud sandbox. Flagging per the card's own AC rather than claiming full
  verification.

## Known, accepted gap — filed as a follow-up, not fixed here

`ut-plugin-language-{de,es}` do not yet carry the 10 new `open_orders.*`
keys or the `orders.title`/`nav.orders` string change. Producing real
German/Spanish translations requires the self-hosted-AI-only NAS model
(ADR), unreachable from this cloud sandbox — same constraint hit by PR
universal-till#970 (ut-docs#1775). Filed as ut-docs (see PR description)
rather than guessing translations past the ADR. `lang-pack-drift` will
show its advisory `::warning::` on this PR; this PR is deliberately **not
merged to `main` this cycle** to avoid leaving `main`'s own
`lang-pack-drift` check red, mirroring the precedent set by PR #970.
