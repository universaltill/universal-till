# receipt.html rendered via text/template — no HTML escaping anywhere on the receipt (ut-docs#906)

**Reviewer**: independent Sonnet subagent, fresh context (`complexity:easy`
— Sonnet built it, a fresh-context Sonnet instance reviewed it, per the
scrum-master skill's model routing exception for easy cards). One round —
see findings below.
**Branch**: `fix/906-receipt-tse-qr-data-uri-escaping` (base: `main`).
**Date**: 2026-08-23.

## What the ticket claimed, and what was actually found

ut-docs#906 reported that `receipt.html`'s TSE evidence QR renders as a
broken image (`src="#ZgotmplZ"`) because `receiptTSEView.QRDataURI` is a
plain `string` interpolated into an `<img src="…">` attribute under
`html/template`'s contextual auto-escaper, which strips `data:` URIs from
a plain string down to the safe placeholder.

**That specific symptom does not reproduce against current `main`.**
Before writing a fix, this card's own `#ZgotmplZ` claim was verified
against the real render path (`renderReceipt` → `receiptTSEView` →
`web/ui/partials/receipt.html`), not just re-asserted from the ticket's
isolated snippet — and it came back negative: a real `qrcode.Encode`'d PNG
data URI rendered through `renderReceipt` untouched, no placeholder.

Root cause of the discrepancy: **`internal/pages/pos_api.go` imports
`"text/template"`, not `"html/template"`** — the only file under
`internal/pages` that does (every other page file in the package correctly
imports `html/template`). `text/template` performs **no HTML escaping at
all** — not URL filtering, not attribute-context escaping, not JS-string
escaping inside the two `onclick="…'{{ .ReceiptNo }}'"` attributes further
down the same file (`receipt.html:153,155`). It just substitutes values
verbatim. So today the QR renders *because nothing is escaping it at
all* — text/template's `data:` URI goes straight through raw. Verified by
grepping every `internal/pages/*.go` import (`html/template` everywhere
else, `text/template` only here), and by directly reproducing both
behaviors: the same template content executed under `html/template`
*does* show `#ZgotmplZ` for a plain-string field; executed via the real
`renderReceipt` (text/template) it does not.

This is a materially bigger defect than the one filed: **every value
interpolated into `receipt.html`** — store name, line item names, TSE
serial/signature fields, discount type, table label, and the `ReceiptNo`
injected raw into two `onclick="...'{{ .ReceiptNo }}'"` JS-string
attributes — currently gets **zero** HTML/attribute/JS escaping. Any of
those containing `<`, `"`, `'`, or `&` (plugin-supplied legal text, a
catalog item name, a table label) is an HTML/attribute-injection primitive
on the receipt surface today. `ReceiptNo` is server-generated so low risk
in practice, but the escaping gap is structural, not tied to one field.

## The fix

- **`internal/pages/pos_api.go`**: import `"html/template"` instead of
  `"text/template"`. `template.FuncMap` is a **type alias** for
  `text/template.FuncMap` (`html/template/template.go:331`), so this is a
  drop-in swap for every other use of `template.*` in the file — no
  signature changes needed at call sites (`httpx.FuncsFor` already returns
  `html/template.FuncMap`, so the two packages were already being crossed
  at the call boundary; this fix makes the file that actually renders the
  template consistent with what calls it).
- **`receiptTSEView.QRDataURI`**: `string` → `template.URL`, set via
  `template.URL(...)` at the one construction site
  (`pos_api.go:1201`→now `1212`). Necessary *because of* the
  `html/template` switch — under real contextual auto-escaping a plain
  string in this position is exactly the ZgotmplZ case the ticket
  described; `template.URL` is `html/template`'s own "this was built by
  us, not from request input" signal for a value that must pass through a
  URL attribute unescaped.
- Added `TestRenderReceipt_TSEQRDataURIRendersNotZgotmplZ` in
  `receipt_test.go`: builds a real `receiptTSEView` via `renderReceipt`
  with a signed TSE signature (so the QR path is exercised, real
  `qrcode.Encode` output, not a stub), asserts the rendered HTML contains
  a real `src="data:image/png;base64,…"` and never `#ZgotmplZ`. This is a
  regression guard for the fixed defect and — since it exercises the real
  `html/template` engine now, not a mock — would also have caught the
  *original* `text/template` gap had it been an assertion on escaping
  behavior generally (it isn't scoped that broadly; the field-by-field
  escaping gap this uncovered is exactly what switching to `html/template`
  closes for the whole file, not just this one field).

No SQL, no i18n keys, no compliance wording, no new page/route — confirmed
by file list (`pos_api.go` + one test file) and by all guards below
passing unchanged.

## Verified beyond automated tests

- Reproduced the ticket's claimed mechanism directly (not just read the
  Go docs): `html/template` executing the literal
  `<img src="{{ .QRDataURI }}">` shape against a plain-string field *does*
  degrade to `#ZgotmplZ` — confirmed with an isolated repro before writing
  any fix.
- Reproduced that the *actual* pre-fix `main` code does **not** hit that
  path: called the real (unexported) `renderReceipt` directly from a
  same-package test, with a real TSE signature and a real
  `qrcode.Encode`'d PNG (not a mock), and inspected the raw output —
  the genuine base64 PNG bytes were present verbatim in
  `<img src="data:image/png;base64,iVBORw0KGgo…">`, no placeholder. This
  is what led to finding the `text/template` import rather than shipping
  a fix for a symptom that wasn't actually occurring.
- Post-fix: reran the same real-`renderReceipt` check — QR still renders
  correctly (now via `html/template` + `template.URL`, not via
  `text/template`'s lack of escaping), confirmed byte-for-byte the same
  real PNG data appears.
- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full CI
  scope minus plugins — green.
- `go test -timeout 20m ./internal/plugins` — CI's separate plugins step
  — green.
- `go test ./internal/pages/...` (the package this diff touches, all
  three subpackages) — green, no behavior change in any other
  `html/template`-rendered page from the `receiptTSEView` field-type
  change (only `receipt.html` references that field).
- Guards: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh` —
  all ✓ (this diff touches neither SQL, i18n strings, compliance wording,
  nor page routes, so unsurprising, but re-run rather than assumed).
- Grepped `internal/pages/*.go` for `text/template` post-fix: zero
  remaining hits — this file was the only one.
- No secret-shaped literal, no real client/shop name anywhere in the diff.
- Also ran the full `go test ./... -race` (beyond what CI itself requires
  — CI does not use `-race`, per `.github/workflows/ci.yml`). Only failure:
  `internal/plugins` timed out at exactly the default 600s per-package
  timeout under the race detector's overhead — a package this diff does
  not touch, and the same package CI itself always gives a dedicated 20m
  timeout for exactly this slowness (wazero/WASM). Confirmed not a
  regression: `go test -timeout 20m ./internal/plugins` (no `-race`,
  matching CI) passed clean in 88s, both before and independent of this
  extra check.
- **Not done**: a live browser render against a real TSE-configured till
  (no TSE hardware/plugin available in this environment). The
  `renderReceipt` real-execution check above (real signature data, real
  `qrcode.Encode` PNG bytes, real `html/template` engine, byte-level
  inspection of the output) is the closest equivalent reachable here, and
  is materially stronger than trusting the ticket's isolated snippet or a
  mocked assertion — but a real receipt screen/print render is still
  worth a human glance before the next TSE-pilot dry run.

## Independent review — adversarial TDD re-verification

The reviewer subagent did not take the TDD claim on faith:

- Reverted **both** changes (import back to `text/template`, field back to
  `string`) and reran the new test — it **passed**, independently
  confirming this record's own claim that the ticket's literal
  `#ZgotmplZ` symptom never reproduced against real pre-fix `main`.
- Reverted **only** the field-type change (kept the `html/template`
  import) and reran — the test **failed**, with the rendered output
  showing exactly `<img src="#ZgotmplZ" ...>`. This proves `template.URL`
  is load-bearing, not decorative: the import swap alone is not the fix,
  the field typing is equally required.
- Restored the fix state and confirmed via `git diff` before reporting.

## Findings

None blocking. One genuine finding, and it's an improvement discovered
*by* the fix, not a regression introduced by it:

- **Security: the `onclick="...'{{ .ReceiptNo }}'"` JS-string attribute
  in `receipt.html` (lines 153, 155) was a live injection primitive under
  the old `text/template` path.** `ReceiptNo` embeds `sync.receipt_prefix`,
  which is admin-writable free text with no allowlist —
  `internal/pages/invoice_page_test.go:505-519` already documents a CSV
  injection PoC (`=cmd|'/c calc'!A1`) against the same field. The reviewer
  rendered `renderReceipt` directly with that same malicious prefix plus
  an XSS payload (`</script><script>alert(1)</script>`) under the fixed
  code: a normal receipt number renders byte-identical to before, and the
  malicious values now come out JS-string-escaped
  (`.../c calc'!A1`, `<script>...`) — the quote and tag
  breakout are neutralized. Under the pre-fix `text/template` path (no
  escaping at all) that same admin-writable field was an unescaped
  attribute/script-breakout primitive on every receipt render. This
  `html/template` switch closes that gap; it does not open one. Not a
  reason to widen this diff — no code change needed beyond what already
  shipped — recorded here because it's a real security property of the
  fix worth knowing about, not because it needs more work.
- No other usages of `receiptTSEView`/`QRDataURI` exist anywhere in the
  repo (grepped both names) — no JSON marshaling or string concatenation
  elsewhere that the `string`→`template.URL` type change could break.
- No existing `receipt_test.go` assertion contains an HTML metacharacter
  in its expected string, so none was at risk of an escaping-format
  change — confirmed by the full green `go test ./internal/pages/...` run
  above (independently rerun by the reviewer, not just trusted).
- One scope note, not acted on: this fix restores correct escaping for
  **every** field `receipt.html` renders, which is strictly more coverage
  than the ticket asked for — not flagged as out-of-scope creep because
  it's the same one-line root cause (the file-level `text/template`
  import) rather than separate work bolted on.

## Safe-to-merge verdict

**Yes.** Root cause corrected (not just the reported symptom patched),
small and self-contained diff, TDD regression test added and independently
re-verified against both the broken and fixed states, full CI-equivalent
gate green, all repo guards green.
