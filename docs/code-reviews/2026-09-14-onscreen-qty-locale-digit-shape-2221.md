# On-screen quantity locale digit-shape (universaltill/ut-docs#2221)

**Card:** universaltill/ut-docs#2221 — `internal/httpx.FormatQty` (locale
digit-shape/grouping for a quantity, the quantity-side twin of
`FormatMoney`) had zero production callers; every real caller used
`FormatQtyLatin` instead. Correct for ESC/POS print paths, but nothing
explained why on-screen quantity display — which DOES get digit-shape
substitution for money via `{{ money }}` — didn't get it for quantities.
Filed as a BA/UX question: genuine gap, or deliberate omission?
**Complexity:** medium (Sonnet dev, Opus review). **Branch:**
`feat/2221-onscreen-qty-locale-digit-shape`.

## Investigation (BA/Architect)

Grepped every `.Qty`/`FormatQty*` call site in `universal-till`. Found a
genuine, confirmed gap: several on-screen (browser-rendered, never
ESC/POS) surfaces rendered a raw Go `%.0f`/`{{ .Qty }}` with no locale
awareness at all — not even `FormatQtyLatin`. Under `fa`/`ar` (both
shipped locales), the money column on these same rows already showed
Persian/Arabic-Indic digits via `{{ money }}` while the quantity column
sat in plain Latin digits — exactly the inconsistency the card asked
about.

Also found a narrower, architecturally interesting sub-case:
`internal/data.KioskCounterOrderLine.Qty` was a single stored value
feeding TWO destinations with **opposite** digit-shape requirements —
`self_order_shop.go`'s kitchen-ticket print (must stay Latin — an ESC/POS
printer can't render Arabic-Indic glyphs) and
`kiosk_counter_orders_page.go`'s staff-facing on-screen "pay at counter"
board (should follow the viewing operator's locale). A single
pre-formatted string could only ever be right for one of the two.

And one surface that must explicitly stay untouched:
`web/ui/partials/basket.html`'s `qty-input` is an *editable* form field —
its value is read back by `/api/pos/line` and parsed with a plain
`strconv.ParseFloat`, and validated client-side against
`pattern="[0-9]+"`. Verified the failure mode directly: a non-ASCII digit
there fails the parse, `qty` becomes `0`, and `0` **voids the line**
(confirmed reading `pos_api.go`'s own comment on that path). Wiring `qty`
into that input would silently delete a basket line on every htmx re-post
under any fa/ar/non-Latin locale.

## What shipped

- `internal/httpx/httpx.go`: new `qty` template func in `FuncsFor(locale)`,
  mirroring `money` — calls `FormatQty(v, locale)` via a new `qtyFloat`
  any→float64 coercion helper (float64/float32/int/int64/int32).
- Wired `{{ qty .Qty }}` into every on-screen, non-editable quantity
  display found: `receipt.html`, `invoice.html`, `order_view.html`,
  `journal_detail.html`, `self_order_cart.html`, the kiosk-counter-orders
  staff board, and the reports tabs (top/slow items, dead stock,
  by-department payments breakdown, and the archived-EOD
  by-article-group/by-article/by-order-type tables).
- `internal/data/kiosk_counter_orders_repo.go`: `KioskCounterOrderLine.Qty`
  changed from a pre-formatted `string` to a raw `float64`, so the kitchen
  ticket (Latin, at print time) and the staff board (locale-aware, at
  render time) format it independently instead of sharing one pre-baked
  string. `printCounterOrderTicketAsync` now formats at print time against
  `httpx.DefaultLocale()` — matching the ticket's other fields
  (Station/OrderLabel/OrderType), which already used `DefaultLocale()`
  rather than the ordering customer's own request locale.
- Deliberately left `basket.html`'s `qty-input` untouched (see
  Investigation above) and `invoice_page.go`'s `formatQty`/`print.Doc`
  path untouched (feeds an ESC/POS-rendered document, correctly Latin).
- `scripts/ci/deadcode-baseline.txt` / `FormatQty`'s own doc comment:
  updated now that it has real callers.
- `make docs-shots` re-run (`internal/pages/**.go` and `web/ui/**` are
  both part of `guard-docs-shots.sh`'s hashed surface) — regenerated
  `web/help/img/manifest.json`. No individual screenshot's pixels changed:
  the demo/seed data used by the docs-shots harness has no populated
  quantity on any of the routed topics it captures (empty counter-orders
  board, zero-sales reporting window, etc.) — confirmed by inspecting the
  regenerated PNGs directly. The regeneration was still required and is
  now current against the changed source surface.

## Verification (beyond automated tests)

- New tests: `internal/httpx/httpx_test.go`'s `TestFuncsForExposesQty`;
  `internal/pages/kiosk_counter_orders_page_test.go`'s
  `TestKioskCounterOrdersPage_ItemsFollowViewerLocaleDigitShape` (asserts
  the real rendered HTML contains `Flat White × ۲` under `?lang=fa` and
  never a Latin `× 2`); `internal/data/kiosk_counter_orders_repo_test.go`'s
  `TestKioskCounterOrdersRepo_ListOpenToleratesLegacyStringQty`;
  `internal/pages/self_order_counter_mode_test.go`'s
  `TestPrintCounterOrderTicketAsync_QtyFollowsShopLocaleNotRequestLocale`.
  All TDD'd: written first, confirmed failing against the pre-fix code
  with the real error message, then made to pass.
- **Live visual check**, not just rendered-HTML-string assertions: booted
  a real throwaway till (`e2e/run-till.sh`), seeded one open counter order
  directly into its DB, and screenshotted `/kiosk-counter-orders?lang=fa`
  with Playwright/Chromium — `Flat White × ۲, Croissant × ۱` renders
  cleanly, correctly RTL-laid-out, no overlap/wrapping/cut-off, and the
  basket screen's own `qty-input` (screenshotted separately under `fa` via
  the regenerated `sell.png`) still correctly shows a plain Latin `1`,
  confirming the basket-input exclusion didn't regress. Did **not**
  separately screenshot `receipt.html`/`invoice.html`/`journal_detail.html`
  live with populated data — these are structurally identical one-line
  `{{ qty .Qty }}` additions to templates that already render `{{ money }}`
  immediately adjacent via the exact same template engine and the exact
  same underlying digit-shape code path (`LocalizeDigits`/`formatGrouped`),
  so the residual risk of a rendering artifact specific to `qty` there,
  beyond what the counter-orders board check already covers, is assessed
  as low — noted explicitly rather than silently assumed covered.
- `gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
  `golangci-lint run ./internal/...` (0 issues), full `go test ./...`
  (every package green, ~13 min), `guard-i18n.sh`, `guard-data-access.sh`,
  `guard-docs-shots.sh`, `guard-page-http-error.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` (pre-existing, tracked drift only, unrelated to
  this diff), `guard-compliance-claims.sh`, `guard-kiosk-engine.sh` — all
  green.
- No new user-facing strings (`guard-i18n.sh` confirms) — this is a
  formatting correction, not a new feature/workflow, so no
  `universal-till/web/help/` prose update is needed; only the screenshots
  needed regenerating (done above).
- No real client/shop name used as test data; no secret-shaped literals.

## Independent review

Opus subagent, isolated git worktree (`.claude/worktrees/review-2221`,
detached at the pre-fix WIP commit), instructed to find real problems and
actually run the gate rather than read the diff. Not a rubber stamp —
found two genuine blockers on the first pass, both fixed and re-verified
before commit:

- **F1 (blocker, fixed).** The `string`→`float64` change to
  `KioskCounterOrderLine.Qty` is a change to a **persisted** JSON shape
  (`lines_json` in `kiosk_counter_orders`), with no tolerance for a row an
  already-open counter order left behind across the upgrade. Reviewer
  reproduced the failure directly: `ListOpen` errors on the whole query
  the instant one legacy `"Qty":"2"`-shaped row is in the open set, 500ing
  the staff board on every 15s poll. **Fix:** added
  `KioskCounterOrderLine.UnmarshalJSON`, accepting Qty as either a JSON
  number (current shape) or a JSON string (legacy — tries a plain
  `strconv.ParseFloat` first, then retries with `,`→`.` folded, covering
  the de/tr comma-decimal locales `FormatQtyLatin` could have written at
  the legacy write time). Verified myself: reverted the fix, watched
  `TestKioskCounterOrdersRepo_ListOpenToleratesLegacyStringQty` fail with
  the exact `json: cannot unmarshal string into Go struct field
  KioskCounterOrderLine.Qty of type float64` the reviewer quoted, restored,
  confirmed it (and the plain-integer + comma-decimal cases together)
  pass.
- **F2 (blocker, fixed).** `guard-docs-shots.sh` failed — the diff changes
  the hashed surface (`web/ui/**`, `internal/pages/**.go`) without a
  regeneration. Reviewer explicitly flagged the guard's own
  "manually confirmed no rendered pixel changed" escape hatch as the
  *wrong* remedy here, since `fa`/`ar` screenshots of a Qty-bearing screen
  genuinely could change. **Fix:** ran a real `make docs-shots` (see
  "What shipped" above for why the actual PNGs came out byte-identical —
  demo data, not a shortcut).
- **F3 (should-fix, fixed).** `026_kiosk_counter_orders.sql`'s header
  comment still said Qty was "a pre-formatted string... this table
  carries no numeric quantity type of its own" — the exact statement F1
  invalidated, and the first thing anyone debugging a legacy-row issue
  would read. Rewrote it to describe the current shape and the
  `UnmarshalJSON` tolerance.
- **F4 (should-fix, fixed by wiring, not narrowing).** `FormatQty`'s own
  doc comment claimed "every on-screen... display" while missing
  `journal_detail.html` and three `reports_tab_*.html` tables (all render
  through `httpx.Render`/`RenderPartial`, so `qty` was already in scope —
  one token per site). Wired all four rather than narrowing the comment,
  since the whole point of the card is on-screen/money-column consistency
  and these are the same shape of screen as the ones already converted.
  Doc comment updated to list the actual, now-accurate, full set.
- **F5 (nit, accepted, not fixed here).** The counter-orders board's
  adjacent Age column (`0 دقیقه`) still renders its number in Latin via a
  different helper (`elapsedMinutes`/`tables.status.open_minutes`) —
  visually confirmed in the live fa screenshot above, sitting right next
  to the now-correct `× ۲`. Real, but a different field (minutes, not a
  sale-line Qty) and a different helper shared with the tables floor plan
  — out of scope for this card. Worth its own follow-up card rather than
  scope-creeping it in here.
- **F6 (nit, addressed).** Moving Qty formatting for the kitchen ticket
  from write time (customer's request locale) to print time (shop's
  `DefaultLocale()`) is a real, externally-observable behavior change for
  a weighed line's fractional qty (decimal separator). Reviewer judged it
  a correction (matches the ticket's other fields, which already used
  `DefaultLocale()`) but flagged it as undocumented and untested.
  Documented inline at the call site and added
  `TestPrintCounterOrderTicketAsync_QtyFollowsShopLocaleNotRequestLocale`,
  pinning the tr/comma-decimal case specifically.
- **F7/F8 (observations, pre-existing, out of scope).** `receiptLine.Qty`
  truncates a weighed line's fraction to `int` before display
  (`pos_api.go:2193`, predates this diff) and `invoice_page.go`'s
  `print.Doc` path feeds `FormatMoney`'s digit-shaped output into an
  ESC/POS render (predates this diff, arguably violates the
  print-stays-Latin invariant this diff's own comments now state
  explicitly). Neither introduced or worsened here; not fixed, to keep
  this diff scoped to the card's actual ask — worth their own follow-up
  cards, not filed as new Backlog cards in this cycle for time reasons.
- Confirmed both standing recurring-bug-class checks: no file-write /
  `os.MkdirAll` / `paths.Data(...)` concern (diff introduces no filesystem
  access at all), and — the actual risk class for this diff — no other
  missed on-screen surface beyond F4 (now fixed) and no editable/parsed
  field wrongly switched to locale digit-shape (the `basket.html`
  exclusion was verified sound, not just asserted).

Re-ran the full gate (build/vet/lint/test/all-listed-guards) after every
fix above, not just the specific case each finding named.

## Verdict

Safe to merge. Two blockers found and fixed by an independently
re-verified TDD cycle each; four should-fix/nit findings resolved or
explicitly deferred with reasoning. No money/tax/security surface
touched; repository pattern, i18n, and offline-first rules all intact
(guards green).
