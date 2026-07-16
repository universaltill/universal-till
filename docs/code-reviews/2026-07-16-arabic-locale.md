# Code review — Arabic (`ar`) locale

Date: 2026-07-16
Branch: `feat/arabic-locale`
Author: automated (Claude)

## What was done

Added `web/locales/ar.json`, a complete Modern Standard Arabic (MSA) translation
of the POS UI, targeting the UAE / Dubai market (prospective enterprise customer:
Ansar Mall, Sharjah).

- **705 keys** translated — the exact key set of the base locale `web/locales/en.json`,
  no missing keys and no orphan keys.
- The file was generated from a validated Python mapping that checks key parity
  against en.json and verifies that every placeholder/non-text token is preserved
  before writing, then dumps in en.json key order with `ensure_ascii=false`.
- Keys are ordered identically to en.json for easy diffing.

## Token / placeholder preservation

Automated check (regex over `%s`/`%d`/`%v`, `{{ … }}`, `<b>`/`</b>`, `{0}`-style)
confirmed identical token multisets between each English value and its Arabic
translation. Notable preserved tokens:

- `receipt.legal.plugin_label`: `%s v%s` kept verbatim → `إشعار قانوني (%s v%s)`.
- Currency marker `(£)` kept as-is in `catalog.price_major`, `inventory.cost_optional`
  (a UI symbol slot, not translated text).
- Confirmation keywords the user must type literally kept in Latin: `RESTORE`,
  `RESET`, `CLEANUP`, `PROMOTE`.
- Technical/code tokens kept verbatim: `PNG/JPEG`, `CSV`, `SHA256`, `.tar.gz`,
  `.tar.sig`, `payment.process`, `ESC/POS`, `IP`, `USB`, `/dev/usb/lp0`,
  `Loyverse`, `Square`, `GDPR`, `QR`, `GitHub`.
- Brand strings kept: `Universal Till`, `Task Runner Technology LTD`.

## RTL

No CSS/template changes were needed. `internal/httpx.IsRTL` already includes `"ar"`,
and the `/menu` language switcher auto-lists available locales. Verified live:
`GET /menu?lang=ar` renders `<html lang="ar" dir="rtl" …>` with Arabic labels
(القائمة، الإعدادات، المبيعات).

## Verification (all green)

- `go build ./...` — clean
- `go test ./...` — all packages pass
- `bash scripts/ci/guard-i18n.sh` — “597 template keys resolve; all locales match en.json”

## Terminology decisions & notes for a native reviewer

- **PIN** → `الرمز السري` throughout (natural MSA for a secret numeric code).
- **Till / register** → `الصندوق` (cash register). `tills.*` list uses plural `الصناديق`.
- **SKU** → `رمز الصنف` as a label; literal `SKU` kept where it appears
  parenthetically as a clarifier (e.g. `designer.receipt.show_sku`).
- **VAT** → `ضريبة القيمة المضافة`; `VAT no` → `الرقم الضريبي`. Note UAE uses TRN;
  the generic term reads well and is understood.
- **Numerals**: Eastern-Arabic-Indic digits (٤ ٨ ٣٠ ٦٠ ١٠ ١٤) used in prose text,
  matching the fa.json precedent. Technical tokens, IPs, phone/version numbers
  and codes left in Western digits.
- **Localised placeholder examples** (receipt header preview): address/phone/tax
  examples were adapted to UAE (Dubai address, `04` phone, TRN-style number) —
  these are example placeholder strings, consistent with fa.json localising the
  same keys.

### Flagged as slightly uncertain (worth a native/Gulf reviewer glance)
- `invoice.credits` (“credits invoice”): rendered `إشعار دائن للفاتورة`. It labels a
  credit-note→invoice reference; wording is inferred from context.
- `refund.original_ref` / `receipt.return_for` (“Return for” / “Refund of”):
  rendered `استرجاع لأجل` — reads naturally but the English is a terse prefix to a
  reference number.
- `plugins.type.*` taxonomy terms (e.g. `theme` → `سمة`, `scheduler` → `مجدول`)
  are standard software Arabic but a Gulf retail audience may prefer some variants.
- `designer.price` “Price (cents)” kept as `السعر (بالسنت)` even though the UAE
  minor unit is the fils — the English string itself says “cents”, so the literal
  was preserved; consider a follow-up if this label is surfaced to AED merchants.
