# 2026-07-31 — Sale screen: on-demand OSK + kiosk cursor hiding (ut-docs#155)

**Branch:** `feat/155-osk-no-auto-open` · **Scope:** `web/public/osk.js`,
`web/public/cursor.js` (new), `web/public/app.css`, `web/ui/pages/index.html`,
`web/ui/layouts/base.html` + 4 standalone kiosk templates, locales ×4,
`internal/pages/index_osk_test.go` (new), e2e `settings-osk.spec.ts` +
`kiosk-cursor.spec.ts` (new).

## What shipped

Field report (product owner): on the till's first page the autofocused
barcode input popped the touch keyboard at load, and the mouse cursor was
visible. This change REVERSES the earlier "OSK catches up with the
autofocused field at init" decision (which an e2e test encoded — that test
was inverted, not deleted):

- The OSK opens only from a deliberate click/tap on an OSK-able field or the
  new `⌨️` `data-osk-toggle` button in the scan row — never from programmatic
  focus (autofocus at load, checkout-start's delayed `.focus()`). The show
  trigger moved from `focusin` to `click` because clicking an
  already-focused field fires no `focusin`.
- Scan input carries `inputmode="none"` (no Android IME at load; scanner
  focus kept via autofocus) — except when OSK mode is `off`, where forcing
  it would leave a touch till with no typing path at all.
- New `cursor.js`: touch-capable devices hide the mouse cursor
  (`html.cursor-hidden`); a real mouse `pointermove` restores it
  (persisted per-session), the next touch re-hides. Loaded by the base
  layout AND the four standalone kiosk templates (login, setup, self-order ×2).
- i18n: `osk.toggle` in en/tr/fa/ar.

## TDD evidence

- Go template test written first, failed against HEAD with the real
  messages, passes after.
- Mutation testing, each caught by exactly the intended test: (A) re-adding
  the init catch-up → load-race test fails; (B) reverting click-gate to
  focusin-show → checkout-start test fails; (C) disabling cursor.js →
  touch cursor test fails.
- A real bug found by a failing test mid-development: `.btn`'s
  `display:inline-flex` defeats the `hidden` attribute — the OSK toggle
  (and, pre-existing, the AI-identify button) rendered while `hidden`.
  Fixed with `.btn[hidden]{display:none}`.

## Independent review (Opus subagent, different model)

Ran build/vet/tests/guards/e2e itself. **2 BLOCKING, 7 should-fix, 4 nits;
all blocking + should-fix addressed:**

1. **BLOCKING, fixed:** my `.btn[hidden]` rule had been inserted *inside*
   the `.btn` block, silently moving `text-decoration`/`line-height`/
   `transition` onto the dead rule — measured computed-style regressions on
   every button (line-height 19.2→23.2px, transitions gone). Restored.
2. **BLOCKING, fixed:** mutation-test scaffolding comment left in shipped
   `osk.js`. Removed.
3. **Fixed:** re-tapping the open field reset the symbol/shift layer and
   re-triggered smooth scroll (caret placement would snap `?123` back to
   ABC). `show()` now no-ops for the already-open field.
4. **Fixed:** retargeting between fields stranded `inputmode="none"` on the
   old field for the page's life (dead native IME on Android). `show()`
   restores the previous field's inputmode; `hide()` shares the helper.
5. **Fixed:** OSK mode `off` + touch device + static `inputmode="none"` =
   un-typable barcode field. Template now drops the attribute when mode is
   `off`; Go test asserts it.
6. **Fixed:** cursor.js only reached base-layout pages — the kiosk's most
   visible screens (login/setup/self-order) have their own `<head>`. Added.
7. **Fixed:** cursor vanished on every navigation for mouse users
   (sessionStorage persistence added).
8. **Fixed:** checkout-start e2e could pass vacuously (`#osk` is built
   lazily; detached = "hidden"). Liveness tail added (click → visible).
9. **Fixed:** dead `htmx:afterSettle` re-reveal removed (scan row is never
   htmx-swapped).
10. **Nit, fixed:** `⌨` U+2328 → `⌨️` (+FE0F) so the Pi's Noto Color Emoji
    covers it (prior tofu-glyph field incident class).
11. **Nit, fixed:** toggle now retargets when the OSK is open for a
    different field instead of needing two taps.
12. **Nit, accepted:** a `data-osk-toggle` outside a `<form>` no-ops by
    design (commented in code).

Review-confirmed clean: RTL (all logical properties), label-click show path,
login/PIN flow independence, `#osk` internal clicks, desktop typing/paste
under `inputmode="none"`, no file writes/SQL/network, embed picks up
cursor.js automatically.

## Verified beyond automated tests

Real server + touch-emulated Chromium: load → no keyboard, scan field
focused, cursor hidden; toggle tap → OSK opens targeting scan field.
Screenshots attached to the ut-docs#155 close-out. Full gate after fixes:
build/vet clean, full `go test ./...` green, both guards green, all 25 e2e
specs green (default + auth projects), ports 8091/8092 clean.

## Accepted gaps / deferred

- The `auto`-mode late-enable (`touchstart` after load on a device that
  reports non-touch) has no automated test — exercised manually by review.
- Real-device pass on the Pi kiosk (glyph rendering, cage cursor behavior)
  rides the existing pending-field-tests card (ut-docs#21).

**Verdict: safe to merge.**
