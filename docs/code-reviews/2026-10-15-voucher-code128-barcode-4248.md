# Review — on-screen voucher barcodes as CODE128 (ut-docs#4248)

**Branch:** `feat/4248-code128-voucher-barcode` · **Author:** Opus 5.5 (lane:cloud-24) · **Reviewer:** Fable, fresh context, own worktree

## What shipped

- `internal/httpx.Code128SVG`: CODE128 SVG renderer, code set B for any
  printable ASCII plus set C for digit runs (4+ at an edge, 6+ mid-code),
  same 60-char / printable-ASCII ceiling as the printed receipt
  (`internal/print` `barcodeEncodable`). 2px modules while the symbol fits
  ~384px, else 1px (no fractional CSS scale); `preserveAspectRatio="none"`
  keeps bars full height under the receipt's 44px rule; `crispEdges`; the
  code is HTML-escaped into `aria-label`.
- Template func `barcode128svg`; `web/ui/partials/receipt.html` uses it for
  issued voucher codes (receipt number stays CODE39).
- Help `vouchers.md` step 6 (en/de/tr/ar/fa): dropped "on screen only for
  numbers the till creates". docs-shots regenerated (manifest only).
- Tests: golden module strings from python-barcode 0.16.1 (independent
  encoder), an independent round-trip decoder (checksum + set switches),
  SVG read-back, 1px/2px switch, escaping, ceilings; receipt test now
  expects a barcode for the lowercase uuid code; e2e
  `voucher-code128-4248.spec.ts`.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | 1px fallback only avoids fractional scaling while the 1px symbol fits; a 36-char uuid (~450px) is still scaled down, so comments claiming uuid support / "never a fractional scale" overclaim | Fixed: comments now state the ~28-char limit and the high-DPR caveat |
| 2 | nit | `httpx.go` comment said "CODE128 B" | Fixed ("sets B/C") |
| 3 | nit | test comment mixed symbol counts | Fixed |
| 4 | nit | decoder doc said it shares only the table | Fixed |
| 5 | nit | odd 5-digit edge run switches to C for zero gain | Accepted: still valid, never longer than plain B |
| 6 | nit | e2e containment assertion holds by CSS regardless of module logic | Accepted: the Go tests pin the 1px/2px switch; e2e is the real-app smoke + decode harness |

The reviewer independently re-typed the ISO 15417 width table (0 mismatches),
round-tripped ~30 adversarial inputs through its own decoder, and confirmed
both the receipt unit test and the e2e fail without the fix.

## Verified beyond automated tests

- TDD: with `receipt.html` reverted, `TestRenderReceipt_IssuedVouchersListed`
  fails ("got 1"); restored, passes.
- Real decode with zxing-cpp of Chromium renders in a 387px / 330px column at
  DPR 1 and 2: 12-char alnum, 13-digit numeric, 20- and 30-char lowercase
  scan (30-char not at 330px/1x); 36/40-char do not (layout limit, see #1).
  Before the 1px fallback, 20-char lowercase failed at 1x.
- e2e in the real app: sold a gift voucher with a 25-char lowercase card
  number, screenshotted the on-screen receipt barcode at 1x — zxing decoded
  it as Code128 with the exact code.

## Deferred

- ut-docs#4403: the printed receipt's CODE128 is set B only at module 2, so
  codes over ~21 chars exceed 80mm paper.

**Verdict:** safe to merge.
