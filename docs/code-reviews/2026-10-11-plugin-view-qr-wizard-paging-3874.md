# Review: plugin view components `qr`, `wizard_step` and table paging (ut-docs#3874)

- **Branch:** `claude/universal-till-build-yvwwi9` (+ the same branch in
  `ut-plugin-language-{de,es,pt}` and `ut-docs`)
- **Spec:** ADR-0121 §7 (build card 8d); format: ut-docs
  `reference/plugin-views.md` (updated in the same change).
- **Author:** Opus 5.5 (cycle orchestrator, inline). **Reviewer:** Fable
  (independent subagent, own worktree). Orchestrator triaged and fixed.

## What shipped

- `qr` `{data, label?}`: data 1–1024 bytes of UTF-8 without NUL; at most 8 per
  document. Core encodes it (go-qrcode, level M, 4 px per module) once, in the
  validator, and draws it as a PNG data URI (`template.URL`) with core's own
  alt text ("QR code") and the label as a `figcaption`. A plugin never sends an
  image or a URL.
- `wizard_step` `{steps, current}`: 2–12 steps, 1-based `current`, at most one
  per document. "Step n of m" with locale digits, `aria-current="step"`, a check
  mark plus a visually-hidden "(done)" on done steps.
- `table.paging` `{page, pages | has_next, param?}`: page 1–1,000,000, exactly
  one of `pages` (≥ page) or `has_next`, `param` (default `page`, field-name
  rule, unique per document). `View.LinkPaging` builds Previous/Next links: the
  entry's own route + the forwarded query with `param` set to page±1, never a
  `_` key, at most 20 keys (= the forward cap, so the page is never dropped).
  Called on plugin pages and after actions; a content-slot panel and the
  read-only setup wizard show the indicator only (slot paging: ut-docs#4114).
- 9 core keys `plugin.view.{qr_alt,wizard_label,wizard_step_of,step_done,
  paging_label,page_prev,page_next,page_n,page_n_of}` in en/ar/fa/tr and the
  de/es/pt packs. CSS in the plugin-view block, logical properties only.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | QR PNG encoding was done in `Prepare` on every draw with no per-document cap: a 200-QR document inside the 256 KiB cap cost ~1.8 s CPU per GET/poll. | **Fixed:** cap of 8 qr per document; encoded once in the validator, the PNG kept on the component. Test `TestQR_CapAndEncodedOnce_3874`. |
| 2 | minor | Fixed 256 px image (and CSS padding) gave ~2.2 px/module for long payloads — unreliable scans. | **Fixed:** 4 px per module (`qrcode.Encode(…, -4)`), drawn at its own size, `max-inline-size: 100%`, no padding (go-qrcode includes the quiet zone). |
| 3 | minor | A paging link with 20 forwarded keys sorting before `page` would lose the page on the next ask (20-key forward cap). | **Fixed:** links keep `param` + the first 19 others; `pluginViewMaxParams` now reads `pluginview.MaxParams` (one source). Test `TestLinkPaging_ParamsCapAndReserved_3874`. |
| 4 | nit | `LinkPaging` relied on its caller to strip `_job`. | **Fixed:** skips every `_` key itself (same test). |
| 5 | nit | `route + "?"` assumed no query in the route. | **Fixed:** `url.URL{Path, RawQuery}`. |
| 6 | nit | Done steps had no accessible state. | **Fixed:** visually-hidden `plugin.view.step_done`. |
| 7 | nit | Dead "QR code" text fallback in the template. | **Fixed:** removed (the validator guarantees an encoded image). |
| 8 | nit | No test that the setup-wizard path draws no links. | **Accepted:** that path never calls `LinkPaging`; ut-docs#4114 (slot paging) adds the panel/wizard link paths and their tests. |

Reviewer also probed (throwaway test): `page` as float/string/exponent refused;
`pages:null` treated as absent; omitted `param` vs explicit `"page"` detected
as a duplicate; paging hrefs escape keys and values (`url.Values.Encode`) and
render in html/template URL context without injection; the data URI renders
without `ZgotmplZ`. MkdirAll / `paths.Data`: N/A (no file I/O).

## Verified beyond automated tests

- TDD: removing the `LinkPaging` call fails `TestPluginView_QRWizardPagingRender_3874`
  (orchestrator and reviewer, independently).
- Driven render: the real page HTML from the handler with the real `app.css`,
  screenshotted in Chromium at 1024×600 and 360×740 in en, fa and tr: no
  horizontal scroll, RTL mirrors the step chips and paging row, Persian digits
  in "صفحهٔ ۲ از ۱۴", paging links at the 46 px touch floor. Not checked on
  touch hardware (no device in a cloud session); the change adds plain links
  and no pointer handlers.
- Not run here: `guard-deadcode-baseline.sh` (this container's toolchain can't
  build `deadcode`; CI runs it). The new exported identifiers are all used.
- Manual (`web/help/`): no change — a plugin page's content is the plugin's;
  the operator-facing manual has no plugin-view topic to update.

## Notes

- The future full CSP (ut-docs#2913, `default-src 'self'`) must allow
  `img-src data:` on `/plugin/*`, or the QR must move to a core route.

**Verdict:** safe to merge.
