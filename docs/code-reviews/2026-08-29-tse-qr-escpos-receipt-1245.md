# Code review — TSE evidence QR code on printed (ESC/POS) receipts (ut-docs#1245)

- **Date:** 2026-08-29
- **Branch:** `fix/1245-tse-qr-escpos-receipt`
- **Reviewer:** independent reviewer (Opus, worktree-isolated — different model
  from the implementer, which built at Fable per this card's `complexity:hard`
  routing)
- **Refs:** ADR-0044 (swappable fiscal signing provider), ut-docs#585 (the
  original HTML-receipt QR + the provisional payload format), ut-docs#1244
  (sibling ticket, closed as duplicate of ut-docs#999 — unrelated: signing
  *dispatch* on refund/return, not receipt *rendering*)
- **Verdict: safe to merge.** One CI-blocking finding and one real compliance-
  relevant defect were found and fixed before merge; both independently
  re-verified after the fix.

---

## What shipped

The physical ESC/POS thermal-printer receipt path never printed a scannable
code for the §6 KassenSichV TSE signature evidence — only plain text lines
(serial, transaction number, counter, algorithm, signature) when a signature
was recorded for the sale. The on-screen/HTML receipt already renders this
evidence as a QR code (ut-docs#585); the ESC/POS path was an explicitly
documented non-goal at the time. This closes that gap:

- `internal/print/escpos.go` — new `Doc.TSEQR []byte` field (a pre-encoded
  `GS v 0` raster block, same shape as the existing `Logo` field). `Render()`
  emits it as its own centered block, after the barcode block and before the
  footer. `RenderText()` (the plain-text preview/CUPS path) deliberately does
  **not** represent it — see B2 below.
- `internal/pages/print_api.go` — inside the existing
  `GetFiscalTSESignature` gate in `buildReceiptDoc` (the same gate that
  already produces the plain-text evidence lines), builds the QR via the
  **existing, unmodified** `buildTSEQRPayload` (ut-docs#585's provisional
  payload format — not touched here, not re-verified against a real TSE,
  still an open, separately-tracked caveat) → `qrcode.Encode` → the
  **existing, unmodified** `print.RasterLogo` (already used for the shop's
  uploaded logo). Any encode/raster failure degrades silently to no QR,
  matching this function's existing "a receipt without X beats no receipt"
  policy.
- `web/help/{en,tr,fa,ar}/sell.md` — the "TSE signature block on receipts"
  topic updated to say both the on-screen and thermal receipt render the QR
  (previously only the on-screen one did); tr/fa/ar hand-translated (the
  project's usual NAS translation model was unreachable from this sandbox),
  reusing each file's own existing terminology verbatim.
- Tests: `TestRenderIncludesTSEQR`, `TestRenderNoTSEQRBlockWhenUnset`,
  `TestRenderTextIgnoresTSEQR` (print package); extended
  `TestBuildReceiptDoc_TSESignatureLinesWhenRecorded` (pages package).

## Findings

### B1 — CI-blocking: `guard-docs-shots.sh` failed (fixed)

The four `sell.md` prose edits invalidated their recorded screenshot hashes
even though no screen actually changed (this is a backend/print-path change,
no UI touched). Confirmed the guard passes on `main` and fails on this branch
pre-fix. **Fix:** ran `make docs-shots` (all 92 topic×locale screenshots,
pre-installed Chromium, ~1.8 min) and committed the refreshed
`web/help/img/manifest.json`. No `.png` content actually changed (git shows
only the manifest's tracked hash moving) — confirms no screen regressed.
Guard now passes: `✓ docs-shots guard: 23 routed topics × 4 locales
screenshotted and fresh`.

### B2 — real defect, not just cosmetic (fixed)

`RenderText` backs two callers, not only the receipt-designer preview:
`internal/print/system.go`'s `PrintDoc` pipes its output verbatim to `lp` for
`printer.mode == "system"` (a regular office/CUPS printer) — a genuinely
different, plain-text-only path with no way to render a raster at all. The
original diff added a fixed `"▓▓ [TSE QR] ▓▓"` placeholder to `RenderText`,
reasoned as "so the designer preview can tell it apart from the barcode." But
the receipt-designer's own sample document (`receipt_designer.go`'s
`sampleReceiptDoc`) never sets `TSEQR` — the preview can never reach that
branch — so the placeholder's only *live* effect was printing that literal
string onto a real customer's TSE-signed receipt on a system/CUPS printer, in
place of the actual evidence. Worse than printing nothing, and a direct
violation of this file's own stated policy (`print_api.go`: "fields the
signer didn't return are skipped — never placeholders") on a p1 compliance
ticket.

**Fix:** removed the placeholder entirely. `TSEQR` now has **no**
representation in `RenderText`, matching `Logo` — the other raster-only
`Doc` field, which `RenderText` has never touched, for the identical reason
(no honest plain-text rendering of a raster exists). The evidence itself is
not lost on a system/CUPS printer: the pre-existing plain-text lines (serial,
transaction no., counter, algorithm, signature) still print unchanged; only
the *scannable* form is thermal/raster-only, which is an accurate reflection
of that printer type's real capability. `TestRenderTextIgnoresTSEQR` replaces
the old placeholder test and asserts the string `"TSE QR"` never appears in
`RenderText` output, plus that the barcode preview around it still renders
correctly.

## Checked and found clean (independent review, re-verified after the B1/B2 fixes)

- **Placement/alignment**: the `TSEQR` block in `Render()` is its own
  `cmdAlignMid`/`cmdAlignLeft` pair, after the barcode block's own closing
  `cmdAlignLeft` — not nested inside it, so the printer is never left in the
  wrong alignment mode. `TestRenderIncludesTSEQR` asserts the raster is
  *immediately* preceded by align-centre, which would fail under nesting.
- **Gating**: identical condition to the pre-existing plain-text evidence
  lines — both inside `if sig, ok, sigErr := ...GetFiscalTSESignature(...);
  sigErr == nil && ok`. Exact parity with the HTML path's `if tseSignature !=
  nil` gate in `pos_api.go`. No stale/looser condition that could show a QR
  for a sale with no (or wrong) recorded evidence.
- **Silent degradation**: `qrcode.Encode` failure or `RasterLogo` returning
  nil (bad/zero-dimension image) both leave `doc.TSEQR` nil/empty; neither
  aborts or corrupts the rest of the receipt.
- **Recurring bug classes**: no `os.MkdirAll`/`Create`/`WriteFile`/`OpenFile`,
  no cwd-relative path where `paths.Data(...)` belongs — this diff writes no
  files at all (mechanically confirmed by grep over the diff).
- **Money / repository pattern / i18n**: no monetary arithmetic, no SQL
  (guard passes), no template strings added — `internal/print` is
  English-literal by this file's own documented convention (`"Subtotal"`,
  `"TOTAL"`, `"TSE serial:"`, …) and isn't scanned by `guard-i18n.sh`.
- **Manual accuracy**: EN wording checked against the actual diff, not just
  read — "alongside the text lines" is accurate (text lines still emit
  unchanged). tr/fa/ar are single-sentence swaps structurally parallel to the
  EN diff, terminology consistent with each file's own prior usage.
- **No real client/shop name, no secret-shaped literal**: test fixtures are
  synthetic (`TSE-TEST-SERIAL-1`, `TESTSIGBASE64==`, `Test Shop`).
- **TDD claim**: independently re-verified by reverting only the production
  hunks (keeping the new `TSEQR` field itself, since removing the field too
  would just be a compile error rather than a meaningful assertion-level
  red) — all four new/changed tests failed with the expected, on-topic
  assertion messages; restoring returned them to green.

## Verification performed

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./internal/print/... ./internal/pages/...` (full packages) | pass |
| `go test ./internal/print/... ./internal/pages/... -race` (scoped to new/changed tests) | pass |
| `guard-data-access.sh` / `guard-i18n.sh` / `guard-help-topics.sh` / `guard-compliance-claims.sh` / `guard-docs-shots.sh` | all pass |
| `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`, `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`, `guard-autofill-suppression.sh`, `guard-makefile-version.sh`, `check-brand-assets.sh` | all pass (unaffected by this diff, run for completeness) |

A full-repo `go test ./... -race` run hit a pre-existing, unrelated
`internal/db.migrate` flake/timeout (documented precedent in
`docs/code-reviews/2026-08-28-fiscal-sign-refund-return-dispatch.md`'s own
review); the `-race` run scoped to the actual new/changed tests above is
clean and is the meaningful check for this diff.

## Explicitly deferred / out of scope (not new — pre-existing, already tracked)

- **QR payload format** (`buildTSEQRPayload`) remains provisional, unverified
  against a real TSE/the authoritative DSFinV-K spec — unchanged by this
  diff, already documented at its own definition, tracked separately.
- **QR physical size** (240px source → ~30mm on 80mm paper): reviewer flagged
  this may be near the reliable-scan floor for a long ECDSA signature payload
  once printed at 1-bit thermal resolution. Worth a field check against a
  real printer before the German pilot goes live; not blocking for this
  ticket (parity with the HTML path, which has the same unverified-in-the-
  field status).
- **`ut-docs#1203`** (add `sale_type`/direction to the `fiscal.sign.ask`
  contract) remains the correct blocker for `universal-till#594` (refund/
  return signing dispatch) — confirmed no file overlap and no interaction
  with this diff. One forward-looking note for whoever picks up #1203/#594:
  once refund signing dispatch lands, this diff's QR will automatically
  start appearing on refund receipts too (it gates on any recorded
  `fiscal_tse_signatures` row, not on `SaleType`) — another reason #1203
  should land before #594, not after.
