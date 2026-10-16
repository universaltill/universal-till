# Review — till translate mode (ut-docs#4370)

- **Date:** 2026-10-16 · **Lane:** lane:local · **Built by:** Opus 5.5 · **Reviewed by:** Fable 5.1 (independent security + UX review)
- **Design:** ADR-0160 §7.
- **Scope:**
  - In translate mode only, `T`/`TM` append an invisible marker carrying the key (Unicode tag characters). `translate-mode.js` turns it into `data-i18n-key` and strips it. Output is byte-identical when the mode is off.
  - The mode is gated by the override editor's "settings" permission and re-checked on every request. It is refused on a self-order kiosk (`display.mode` and `/self-order*`) and in the public demo.
  - Alt+click, a 600 ms long press or Alt+Enter opens a non-modal side panel (`.show()`). It shows the key, area, description, placeholders, length limit, current and English text, an **Open in Weblate** link plus a till-generated QR code (`/translate/universal-till/<area>/<lang>/?q=key:="<key>"`, checked against the live site), a shop override, and the page's areas.
  - The till holds no Weblate token and makes no cross-origin calls.
  - 30 `translations.mode.*` keys with metadata (en/tr/ar/fa); help topic `translate-mode` in 5 locales; `/translations` screenshots regenerated.

## Verified
- `go build/vet/test ./...`, golangci-lint v2.14.0 (0 issues), and the guards: i18n, i18n-meta, help-topics, help-drift, no-showmodal, rtl-logical-css, no-inline-handlers, data-access, docs-shots.
- e2e `translate-mode-4370.spec.ts` 9/9, plus the sell-tile jiggle spec, 12/12, at 1024×600 and 360 px.
- The kiosk refusal is server-side and tested (403 even with the cookie). Removing the kiosk check fails 3 tests.
- The reviewer audited print, fiscal, JSON and stored paths: none receives the wrapped `T`, so no hidden characters reach a receipt or TSE payload.
- **docs-shots:** a full 120-shot run of the branch and of clean HEAD in the same amd64 container (Chromium 141 headless shell) gives `translations.png` as the only differing PNG. So only that topic's PNGs are committed. en and tr reproduce the committed set byte-for-byte; fa and ar can't be byte-reproduced off the original reference container (Arabic-script shaping), so their new PNGs are correct but rasterised slightly differently.
- **Not done:** a real long press on the TECLAST tablet (not on adb), the dark theme, and the German layout.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | Medium | Android fires `contextmenu` at ~500 ms then `pointercancel`, so the panel would never open on the tablet | Fixed: a `contextmenu` during a press counts as the long press; `user-select: none`; e2e reproduces the Android sequence |
| M2 | Medium | Long press on a sale tile also entered quick-button edit mode | Fixed: `app.js` skips its hold in translate mode; e2e with a control run |
| L1 | Low | Demo comment contradicted the route lists | Routes moved to denied; `translateModeOffered()` is false on demo and kiosk; tested |
| L2 | Low | Chooser `hx-get` values not URL-encoded | `urlquery`; tested with `& # + %20` |
| L3 | Low | Decoder stripped tag characters outside markers (flag emoji) | Only inside markers; e2e with the England, Scotland and Wales flags |
| L4–L8 | Low | CSRF nuisance with auth off; per-request DB reads; a11y affordances; QR as a data: URI under report-only CSP; override validation parity | Accepted as noted by the reviewer; a11y documented in the help topic |

## Follow-ups
- Language-pack PRs for the 30 new keys (de/es/pt) right after merge.
- Tagging HTMX fragments rendered outside the httpx helpers.
- `en-US` picker shows "ar" (pre-existing).

**Verdict:** safe to merge.
