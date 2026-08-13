# Review — kitchen ticket i18n (ut-docs#261)

## Summary

Kitchen tickets (`internal/print/kitchen.go`, built from
`internal/pages/kitchen_print.go`) printed a handful of hardcoded English
words regardless of the shop's configured locale: the default station
header ("KITCHEN"), the order label ("ORDER"), and the order-type text
("TAKEAWAY" via `strings.ToUpper(detail.OrderType)`). Dine-in sales
(`order_type == ""`) printed no order-type line at all, which was correct
but undocumented/untested.

Product owner's decision (2026-08-12, on the issue): localize kitchen
tickets — order-type text and other fixed strings stop being raw English;
dine-in keeps printing nothing (no explicit "DINE-IN" label, since absence
already reads as dine-in, a normal kitchen convention).

## What changed

- `internal/print/kitchen.go`: `KitchenTicket` gained an `OrderLabel`
  field (pre-translated "ORDER" word, defaults to "ORDER" if unset — so
  existing/i18n-agnostic callers and tests are unaffected). `OrderType`'s
  doc comment updated: it's now pre-translated, display-ready text
  supplied by the caller, same "callers translate labels" convention as
  `Timestamp`. Render functions are otherwise structurally unchanged — no
  i18n dependency added to this package, stays byte-testable.
- `internal/pages/kitchen_print.go`: new `kitchenOrderTypeLabel` maps the
  persisted `order_type` to display text (only `pos.OrderTypeTakeaway`
  exists in the domain today; unrecognized future values fall back to the
  raw string rather than erroring). `kitchenTicketFor` resolves
  `httpx.DefaultLocale()` (the shop's one configured locale — already
  wired in production from `cfg.Locales.Locale`, confirmed in
  `internal/pages/init.go`) once per print job and threads it through.
- `web/locales/{en,ar,fa,tr}.json`: two new keys,
  `kitchen.ticket.order_label` / `kitchen.ticket.station_default`. en
  values are `"ORDER"`/`"KITCHEN"`, byte-identical to what was previously
  hardcoded — the zero-stations byte-pinned test needed no change.
- `web/help/{en,ar,fa,tr}/printing.md`: one sentence added noting kitchen
  tickets follow the till's configured locale.
- `docs/architecture/receipt-printing.md` (this repo's doc, not
  `universal-till`'s — see below): extended the existing "Character sets
  (honest v1)" section to cover kitchen tickets now carrying the same
  non-Latin/`ascii`-charset risk receipts already document.

## Independent review (Opus, isolated worktree)

One round — no blocker-class (money/data-loss/security) finding, so no
second round was required per the pipeline's process-depth rule. All 9
findings fixed in this same session; none deferred.

**Should-fix (4), all fixed:**

1. **Real bug**: `kitchenTicketFor` detected "the default bucket" by
   comparing the station *name* to the `kitchenStation` sentinel
   (`station == kitchenStation`). Nothing stops a shop naming a real
   station literally "KITCHEN" (`kitchen_stations_repo.go` does no name
   validation) — that station's ticket would then get silently
   *translated* instead of printing verbatim, contradicting this exact
   change's own comment that a shop-entered name is never translated.
   Reviewer confirmed empirically with a throwaway probe under a `tr`
   default locale. **Fixed**: `kitchenTicketFor`/`kitchenTarget` now carry
   an explicit `isDefault bool` instead of a value comparison. Regression
   test added: `TestPrintKitchen_ShopStationNamedKitchenStaysUntranslated`
   (a real `data.KitchenStation` named "KITCHEN", routed via
   `SetItemStationRoutes`, asserted to print verbatim under `tr`).
2. **Doc drift**: the change lets kitchen tickets carry non-Latin text
   through the same `utf8`/`ascii` ESC/POS path this repo's
   `architecture/receipt-printing.md` already documents as unreliable for
   Farsi/Arabic in text mode — that doc wasn't updated, and the deleted
   `kitchenStation` comment ("printed on thermal paper, latin, like the
   receipt's labels") was the thing keeping kitchen tickets out of this
   risk class in the first place. **Fixed**: extended that doc's
   "Character sets (honest v1)" section with an explicit kitchen-ticket
   note (same file this review is filed alongside is `universal-till`'s;
   the architecture doc itself lives in `ut-docs` and was edited there in
   the same session).
3. **Real regression**: on `printer.charset == "ascii"` (a real
   Settings-exposed option), a non-ASCII translation (ar/fa) now prints as
   a run of `?` via `encodeText`'s unmappable-rune handling — worse than
   before, when the hardcoded English text was always legible regardless
   of charset. **Fixed**: added `kitchenTicketText`/`isASCII` — on
   `ascii` charset, a non-ASCII-safe translation falls back to the English
   text (always ASCII for this ticket's own keys) instead of garbage.
   `utf8` (the default) is unaffected. Regression test:
   `TestBuildKitchenTicket_AsciiCharsetFallsBackToEnglishForNonLatinLocale`
   (Arabic default locale + `printer.charset=ascii`, asserts English
   fallback text). TDD-verified: reverting the fallback made the new test
   fail with the actual Arabic strings as the "got" value; restored and
   green.
4. **Help-doc overclaim**: the added `printing.md` sentence implied a
   shop could have kitchen staff read a *different* language than the
   admin UI ("kitchen staff who don't read the admin language still get
   ... they understand"). There's no separate kitchen-locale setting —
   it's the same single `httpx.DefaultLocale()` the admin UI defaults to;
   the two only diverge as a side effect of a manager's per-browser
   `?lang=`/cookie override. **Fixed**: reworded to the accurate claim
   (prints in the till's configured language instead of always English;
   no separate kitchen-only setting) in all four locales.

**Nitpicks (5), all addressed:**

5. `TestPrintKitchen_ZeroStations_ByteIdenticalLegacyTicket` asserted
   `legacy.Station != kitchenStation` — comparing the *printed* (now
   translated) header against the *fixed audit identifier*, two
   deliberately different things after this change. It only passed
   because en's translation happens to equal the constant's value.
   **Fixed**: asserts against `httpx.T(httpx.DefaultLocale(),
   "kitchen.ticket.station_default")` instead, so it stays meaningful
   under any default locale.
6. Reusing `basket.order_type.takeaway` (not a new dedicated key) couples
   kitchen-ticket wording to basket-UI wording with no test pinning that
   coupling. Accepted as-is — the reuse is intentional (same displayed
   concept, real translations already exist in all locales); a dedicated
   key can be split out later if kitchen wording ever needs to diverge.
   Not fixed; noted here as a deliberate choice, not an oversight.
7. The blank-`OrderType` regression test only asserted line-count on the
   ESC/POS byte path; the text-preview path (`RenderKitchenTicketText`)
   was checked only via `!strings.Contains(txt, "TAKEAWAY")`, which a
   stray blank centered line would trivially satisfy. **Fixed**: mirrored
   the line-count assertion for the text path too. TDD-verified:
   simplifying the text renderer's guard away from an `if s != ""` check
   made the new assertion fail with the real line-count mismatch;
   restored and green.
8. `TestBuildKitchenTicket_DineInOrderTypeStaysBlank`'s comment claimed
   more than the test proves — reviewer confirmed by deliberately
   reverting the translation call entirely: this test still passed (the
   raw persisted value for dine-in is already `""`), only
   `IncludesOrderType`/`OrderTypeAndLabelsFollowShopLocale` caught that
   regression. **Fixed**: comment corrected to say what it actually
   guards and points at the tests that cover the rest.
9. `kitchen.ticket.order_label`/`station_default` values must be
   pre-capitalized by the translator (the renderer only upper-cases
   `OrderType`, not `OrderLabel`/`Station`) with no comment flagging that
   the casing is load-bearing for anyone editing these keys via the
   in-product translation editor. Accepted as a documentation gap, not
   fixed in code — noted here; a future translation-editor UI hint is a
   small, separate follow-up if it turns out to matter in practice.

## Verified beyond automated tests

- TDD claims re-verified independently by the reviewer (not just trusted
  on the implementer's word): both original regression tests
  (`TestBuildKitchenTicket_IncludesOrderType`/
  `_OrderTypeAndLabelsFollowShopLocale`, and
  `TestRenderKitchenTicketBlankOrderTypePrintsNothing`) were confirmed to
  fail with real diagnostics when the underlying fix was reverted, then
  pass again restored. Same discipline applied to both fixes made during
  review (findings 3 and 7 above).
- Audit-vs-print separation traced end-to-end: `kitchenTarget.station` is
  never reassigned in `buildKitchenTargets`; `kitchenSendFailure.Station`
  and the `InsertAudit` "station" field both still receive the fixed,
  untranslated `"KITCHEN"` constant regardless of locale — only the
  ticket that's actually rendered gets the translated header.
- `.OrderType` grepped across `internal/` to confirm no other reader
  assumes the print-package field's now-pre-translated semantics — every
  other `.OrderType` belongs to a different struct (`data.SaleDetail`,
  `pos.Basket`, etc.) and still carries the raw domain value unaffected
  (e.g. `web/ui/partials/orders_list.html`'s `eq .OrderType "takeaway"`).
- Full `internal/pages` package suite run (not just kitchen-filtered) to
  confirm the new locale-switching tests' `defer httpx.InitI18n(...,
  "en")` restores cleanly and doesn't leak into any other test — green.
- Translations (ar/fa/tr, both original and the review's added English
  fallbacks) checked for plausibility, not just key-set presence — real
  words for "order"/"kitchen" in a food-service context, no placeholder
  or English-leakage found.
- No real client/shop name or secret-shaped literal introduced (scanned
  the diff).

## Known, accepted gaps (not fixed here — out of scope or needs a human)

- **Real-hardware thermal-printer verification is unverified** — no
  physical printer in this cold cloud pipeline session, for any locale.
  The `ascii`-charset fallback (finding 3) is a software-level mitigation
  for a known limitation, not a claim that ar/fa/tr now render correctly
  on real hardware under `utf8` charset either — that was already an
  open, documented gap for receipts before this card, extended here to
  kitchen tickets by the doc update above.
- **`strings.ToUpper`'s Turkish dotted/dotless-I quirk** (flagged in a
  code comment, not fixed): Go's `ToUpper` isn't Turkish-locale-aware.
  Low-severity cosmetic risk on `tr` order-type text specifically; needs
  a human with real Turkish-locale hardware to confirm it's not actually
  a legibility problem before it's worth a fix.
- **Separate kitchen-only locale setting**: explicitly out of scope per
  the product owner's decision — `httpx.DefaultLocale()` (the shop's one
  configured locale) is deliberately reused rather than introducing a new
  setting/UI/migration for a "kitchen language" nobody has asked for yet.

## Deviation: hand-patched `surface_sha256`, did not run `make docs-shots`

CI's `build` check failed on first push:
`guard-docs-shots.sh` treats every non-test `.go` file under
`internal/pages/` as part of the manual-screenshot surface (deliberately
coarse, per its own header comment — it can't tell whether a Go change
affects rendered output), so editing `kitchen_print.go` tripped it even
though this change touches zero HTML/CSS/JS. Same class of false
positive as ut-docs#620 (`internal/pages/import_page.go`, a prior pure
backend fix) — that issue is still open, tracking a real fix (per-
function granularity, or syncing the cloud session's Playwright/Chromium
versions so `make docs-shots` can actually run here); this session hit
the identical wall it describes: pre-installed Chromium is revision 1194,
`e2e/package-lock.json` pins a Playwright needing revision 1228, and
cloud sessions are instructed not to `playwright install` (network
download).

Followed #620's own precedented workaround rather than inventing a new
one: verified `git diff --stat web/ui/ web/public/` is empty (zero pixel-
surface files touched — confirmed both by directory diff and by checking
`printing` isn't even a routed topic, so the per-topic hash check never
applied to the `web/help/*/printing.md` edits either), then hand-
recomputed just `surface_sha256` using the guard script's own hashing
function and wrote it into `web/help/img/manifest.json` — a one-line
diff, not a real screenshot regeneration. `bash
scripts/ci/guard-docs-shots.sh` passes clean with this patch.

## Verdict

Safe to merge. Full gate green: `go build`/`go vet` clean, `go test
./internal/print/... ./internal/pages/... ./internal/pages/catalog/...
./internal/pages/common/...` green, all 5 CI guards
(`guard-data-access.sh`, `guard-kiosk-engine.sh`,
`guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh`)
green. One known-unrelated pre-existing intermittent flake in
`internal/plugins` (ut-docs#643, reproduced on `main` before this branch
existed, not touched by this diff) — not a blocker for this card.
