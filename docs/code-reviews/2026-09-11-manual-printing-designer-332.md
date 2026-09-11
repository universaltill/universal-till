# Manual content: printing & the receipt designer (ut-docs#332)

**Date:** 2026-09-11
**Card:** ut-docs#332, part of the illustrated user manual epic (ut-docs#324)
**Complexity:** medium (Dev: Sonnet, Review: Opus, per `scrum-master`'s model-routing table)

## What shipped

Rewrote/expanded the in-app manual's printing and receipt-designer coverage:

- `web/help/en/printing.md` — printer setup + test print, reprinting a receipt
  (cross-linked to the Journal, not duplicated), printing shelf/price labels
  (new — verified against the real Catalog "Print labels" tab and
  `POST /api/print/labels`), kitchen tickets (including the honest fact that
  there is no resend button today), and a "Nothing is printing"
  troubleshooting section covering every failure mode `Print test receipt`
  can actually report.
- `web/help/en/designer.md` — retitled from "Receipt & screen designer" to
  "Receipt designer" (the page has no screen/button-layout feature at all —
  that's a separate page, `till-designer.md`/`/designer`) and rewritten with
  real step-by-step coverage: header/footer/logo, live preview and its real
  limits, save-vs-test-print semantics, and manager gating.
- Small cross-link fixes in `reports.md`, `catalog.md`, `till-designer.md`
  (including a stale German cross-reference to the old "Beleg- &
  Bildschirmdesigner" title).
- `scripts/ci/i18n-baseline/help-drift-baseline.json` — 8 entries recording
  that ar/de/fa/tr translation of `printing`/`designer` is deferred to
  ut-docs#341, matching the precedent set by ut-docs#329.
- `web/help/img/manifest.json` — refreshed after regenerating the `designer`
  topic's screenshots (content-only change; no PNG pixels actually differ,
  since no application UI changed).

No application code, templates, or CSS/JS changed.

## Verification

**Dev** booted a real till (`e2e/run-till.sh`, port 8091, `UT_AUTH=off`,
demo catalog) and drove the real screens/endpoints (curl) rather than
inferring behavior from source, confirming exact error strings for every
printer-off/unreachable/USB/system-mode failure, label-printing's copies
clamping and error states, and the full `/receipt-designer` markup and
manager gating.

**Independent review (Opus, fresh context)** re-booted the same harness
independently and reproduced the load-bearing claims itself (all four
printer-failure error strings verbatim, copies clamping, manager gating,
the dead `/api/print/kitchen` endpoint, the absence of any paper-out
detection), rather than taking the Dev report on trust. It also went
further and caught seven real defects the first pass missed — see below.
Guards and tests were re-run independently, not just re-quoted:

```
✓ help-topics guard: no route conflicts, every topic parses, all shipped locales complete, every page route has a claiming topic
✓ help-drift guard: every translated topic's structure matches English (or is recorded, current, in the baseline)
✓ docs-shots guard: 30 routed topics × 4 locales screenshotted and fresh
gofmt -l .            (clean)
go build ./...        exit 0
go vet ./internal/pages/...  exit 0
go test ./internal/pages/... -run 'TestEveryTopicResolves|TestManualIsTranslatedInEveryShippedLocale|TestRouteRegistryResolvesKnownPages'  PASS
```

## Findings from independent review — all fixed before merge

1. **The receipt-designer preview can never show a logo, but the draft said
   it did.** `internal/print/escpos.go`'s plain-text renderer has no logo
   representation at all; a captured real test-print job showed the logo
   raster (`GS v 0`) going out even though the preview never rendered it.
   Fixed: `designer.md` now says the preview never shows the logo (by
   design — it's plain text) and tells the operator to trust a real test
   print instead.
2. **"Sends exactly what the preview shows" was wrong** for the same
   reason (logo-only case). Fixed alongside #1.
3. **The designer's own test-print button doesn't "behave identically" to
   Settings → Printer's** — it only ever reports a bare "Print failed",
   never the detailed reason (confirmed: `receipt_designer.go` emits the
   bare `settings.printer.test_failed` key; `print_api.go` appends
   `": " + err.Error()`). Fixed in both `designer.md` and the
   "Nothing is printing" section: point the operator at Settings →
   Printer's own test print to see the specific cause.
4. **"(or printing a label)" was wrong** — labels report a plain
   "Print failed" on printer-off, not the detailed message. Removed the
   parenthetical and added a preface to "Nothing is printing" explaining
   which buttons show the detailed message and which don't.
5. **Real product gap: labels cannot print at all on "Regular printer
   (system)" connection** — `NewTransport` has no `"system"` case, so
   label printing always fails there with no explanation. This is an app
   bug, not a doc bug; filed as ut-docs#2062, and `printing.md` now
   documents it as a known gap rather than silently overclaiming
   completeness ("cover every way a print can fail" was accordingly
   softened to scope the list to what `Print test receipt` itself
   reports).
6. **The kitchen-warning cross-link over-promised.** The receipt ⚠
   genuinely clears (reprint from the Journal); the kitchen ⚠ has no
   clearing mechanism in the product at all — its only clearer,
   `POST /api/print/kitchen`, has zero UI callers. Filed as ut-docs#2063
   (the dead-endpoint fact itself was already on record in
   `docs/code-reviews/2026-08-12-kiosk-order-print-visibility.md`, but the
   consequence — a permanently-stuck warning — was not). `printing.md` now
   says plainly that the kitchen ⚠ doesn't clear itself.
7. **The rename's locale fallout was left half-done.** `ar/de/fa/tr
   designer.md` still carried the old "Receipt & screen designer"
   title/summary/body (a screen-designer step that doesn't exist), and
   `de/till-designer.md` still cross-referenced the old German title and a
   stale settings path. Fixed: titles now match the real in-product
   strings (`designer.receipt.title`) where one exists (ar/fa/tr), the
   `de` title corrected to match the same meaning, the false
   screen-designer step removed from all four locale bodies and replaced
   with a short cross-link to `till-designer.md`, and the stale German
   cross-reference corrected. `scripts/ci/i18n-baseline/help-drift-baseline.json`'s
   four `designer` entries updated to match the new (smaller) structural
   signature — re-verified green against `guard-help-drift.sh`.

Minor nits also fixed: a misquoted success string ("Print test receipt
sent" → the real "Test receipt sent — check the printer"), the Journal
reprint's own bare-failure-message caveat, the Footer field's 42-character
cap (previously undocumented), and a clumsy self-referential cross-link
sentence.

## Verified beyond automated tests

- Every printer-failure error string in the manual reproduced verbatim
  against a live till by both Dev and the independent reviewer,
  independently.
- The logo/preview mismatch (finding 1) was confirmed by capturing actual
  ESC/POS bytes off a throwaway TCP listener standing in for a printer.
- The dead `/api/print/kitchen` endpoint and the absent paper-out
  detection were confirmed by source + grep, not assumed.
- All cross-links resolve to real topics with matching titles/anchors.
- No real shop/client name, no secret-shaped value anywhere in the diff.

## Deferred (not blocking this merge)

- Translation of the new `printing`/`designer` English content into
  ar/de/fa/tr — tracked on ut-docs#341, same convention as ut-docs#329.
- ut-docs#2062 (label printing broken on system-printer mode) and
  ut-docs#2063 (kitchen-print-failed warning never clears) — both real
  product gaps surfaced by this review, filed as separate Backlog cards
  rather than expanded into this docs-only card's scope.

## Verdict

Safe to merge. No blocker-class (money/tax/data-loss/security) findings,
so a single review round is sufficient per `scrum-master`'s process-depth
guidance — all seven confirmed findings were fixed directly rather than
requiring a second full review pass.
