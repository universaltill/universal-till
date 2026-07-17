# Code review — on-screen keyboard (touch/kiosk tills)

**Date:** 2026-07-17
**Branch:** `feat/on-screen-keyboard`
**Ask (Farshid):** "will we have on screen keyboard for the pos when needed" —
needed especially for the Pi kiosk (cage/chromium has no OS keyboard at all).

## What changed

- `web/public/osk.js` (+ CSS in app.css): self-contained, vendored,
  offline (ADR-0003) keyboard fixed to the bottom of the screen. Shows on
  focus of text-like inputs/textareas, hides on blur; keys never steal
  focus (pointerdown preventDefault). Writes via `setRangeText` and fires
  `input` (HTMX/Alpine listeners run); Enter = `form.requestSubmit()`
  (normal submit path, incl. htmx). Backspace, shift (locale-aware
  uppercase, e.g. Turkish i→İ via `toLocaleUpperCase`), symbols layer.
- **Layouts by page locale** (`<html lang>`): en QWERTY, tr Q-klavye
  (ç ğ ı ö ş ü), fa Persian, ar Arabic (native digit rows for fa/ar);
  **numeric pad** for number/tel/inputmode-numeric fields. Inputs can opt
  out with `data-osk="off"`.
- **Mode setting** `display.osk` = `auto` (default: only when the device
  reports a coarse pointer — real touch screens) | `on` | `off`. Rendered
  as `data-osk` on `<body>`; Settings → Display gains the selector;
  `POST /api/settings/osk`. Follows the UIScale pattern end-to-end
  (RuntimeState, httpx template func, boot init). `display.*` settings are
  per-till and never sync — correct for a hardware property.

## Verification

- Full suite + i18n guard (618 keys, 4 locales) green.
- Live smoke: body renders `data-osk="auto"`, `/public/osk.js` serves,
  Settings shows the selector, `POST /api/settings/osk mode=on` persists
  and re-renders `data-osk="on"`.
- Real touch interaction needs a touch device (the Pi with touchscreen) —
  same field-test list as the kiosk. Desktop browsers with `auto` see no
  keyboard by design (fine pointer).
