# UX — ut-docs#4160: asked choice (`ui.settings.options.ask`) on the plugin settings page
Builds on architect.md (§ refs are to it). The Test-connection parts are out of scope (#5961).

## 0. Surfaces and who is there
- `/plugins/{id}/settings` is an admin page used by a manager. It runs on **(a) the till touchscreen in admin**,
  kiosk and windowed (1024×600 floor, finger, OSK), **(b) desktop back-office**, and **(c) phone at 360px**.
  It is never on self-order. The status bar, lock and exit stay reachable: this design adds no dialog, overlay or modal.
- The real case: an AI plugin (#4110) lists models only after it has an API key and a provider. If the operator sees
  an empty or stale dropdown and nothing explains it, they type around it or pick something wrong (evidence 1).
  So every state below says what is happening and what to do next.

## 1. Interaction states (one asked `choice`, wrapper `[data-ps-ask]`)
| # | State | What renders | Message line (`#ps-opts-msg-KEY`) |
|---|---|---|---|
| S0 | First render (GET, no JS too) | `<select>` holds "not set" (if empty) or the stored value, plus the static options (§1d merge). Usable at once. | empty |
| S1 | Loading (request in flight) | The select stays enabled and keeps its value. `aria-busy="true"` is on the select. The `.htmx-indicator` line "Loading choices…" shows only after **400 ms** (CSS animation delay), so a cache hit never flashes. | unchanged |
| S2 | Loaded | Stored value selected. Then the asked options in answer order, then any static options that are not duplicates. | cleared |
| S3 | Empty list: answered `[]`/declined **and** nothing to choose except "not set"/stored | as S2 | `options_empty` hint (no retry) |
| S4 | Failure (timeout 5 s, error, panic, malformed) | Stored value + static options. Save still works. | `options_failed` + **Try again** button |
| S5 | Saved value not in a non-empty answer | The stored value stays first and selected (never rewritten). Its label is the raw value. | `options_saved_missing` |
| S6 | A `depends_on`/`show_when` field changes (`change`, i.e. blur for text) | Back to S1 → S2–S5 for the new values. The old list stays usable until the answer arrives. If the current pick isn't in the new list, S5 tells the operator to re-pick. | per result |
| S7 | Asked field hidden by `show_when` | No ask. When it shows (controller change), it asks once (S1). | — |
| S8 | After Save (200 or 422 swap) | `htmx.onLoad` asks again. The cache was dropped on save, so a new API key refreshes the list. A 422 keeps the operator's pick (§1f). | per result |

The message line shows one message at a time, in this order of precedence: failed > saved_missing > empty.

**Amendments to the Architect (needed for UX), stated explicitly:**
- **A1 Loading line.** Add key `plugins.settings.options_loading`. The Architect had no loading text. Without one, a
  5 s cold ask looks like "the plugin has no models". The line is an `htmx-indicator` inside the wrapper and is **not** a
  live region (that would be noise). Screen readers get `aria-busy` instead.
- **A2 Empty-state hint (S3).** Add key `plugins.settings.options_empty`. This is not the failure notice, so BA AC 7
  ("no notice") still holds for `options_failed`. Show it only when the select offers nothing beyond "not set"/stored.
  The view decides this; no extra flag is needed from the ask.
- **A3 Saved-missing hint (S5).** Add key `plugins.settings.options_saved_missing`. This covers a stale model id or a
  provider switch. Show it only when the answer was non-empty and not failed.
- **A4 Retry.** Add key `plugins.settings.options_retry` and a `<button type="button" class="btn secondary"
  data-ps-ask-retry="KEY">`. A delegated click handler (no inline handler) triggers `ps-reask` on the matching wrapper.
  Failures are not cached, so retry really re-asks. Without the button, the only retry is a page reload, which
  **discards unsaved edits**.
- **A5 Live region outside the swap.** An element with `role="status"` that is inserted together with its text is often
  not announced. So the message container `<div class="ps-ask-msg" id="ps-opts-msg-KEY" role="status">` lives in the
  form **after** the wrapper and is always present (empty on GET). The fragment fills it with
  `hx-swap-oob="innerHTML"` (text + retry button). The select gets `aria-describedby="ps-opts-msg-KEY"`, plus
  `ps-err-KEY` when there is a field error. The Architect's in-wrapper `<p role="status">` is replaced by this.
- **A6 The operator's pick wins.** The wrapper records the select's value when the request starts
  (`htmx:beforeRequest`). In `htmx:beforeSwap`, if the value changed since then, set `shouldSwap=false` and
  re-trigger `ps-reask`. The form now posts the new pick, which §1g keeps selected. That costs at most one extra ask.
- **A7 Stable id.** Give the select `id="ps-f-KEY"` so htmx restores focus across the innerHTML swap. After a retry swap,
  if focus was on the retry button and the button is gone, focus `#ps-f-KEY`. No other code moves focus.
- **A8 Bidi.** Put `dir="auto"` on each `<option>` whose label the plugin supplied (model ids are LTR inside `fa`/`ar` pages).
- **A9 Fragment HTTP error** (401/403/404/500 from the options endpoint). A 401 goes to `/login`, as the permission
  script does. Anything else shows `options_failed` + Try again. The JS text comes from a page-local
  `var T = { optionsFailed: "{{ T "plugins.settings.options_failed" }}", retry: "{{ T "plugins.settings.options_retry" }}" }`.

## 2. Copy (en, `web/locales/en.json`; meta `review: standard`, area = plugins)
| key | en | max_length |
|---|---|---|
| `plugins.settings.options_loading` | `Loading choices…` | 40 |
| `plugins.settings.options_failed` | `Couldn't load the choices from the plugin. Your saved choice is kept and you can still save.` | 160 |
| `plugins.settings.options_retry` | `Try again` | 24 |
| `plugins.settings.options_empty` | `The plugin has no choices to offer yet. Fill in its other settings and save — the list refreshes after saving.` | 180 |
| `plugins.settings.options_saved_missing` | `The saved choice isn't in the plugin's list any more. It stays saved until you pick another.` | 160 |

- The existing "not set" entry (`plugins.settings.secret_empty`) is reused.
- Plugin labels are plain text: escaped and never translated by core.
- No emoji or icon carries meaning; the messages are text.
- Meta descriptions should say where each text shows: under a plugin-settings dropdown whose list comes from the plugin.

## 3. Touch, 360px, RTL, keyboards
- **Select:** keep the base `min-block-size: 46px` (ut-docs#3882). Kiosk must not be smaller. Full width under its label
  (the label stacks, ut-docs#300), with `max-inline-size: 100%` at every width, not only in the t-cards media query. A
  128-byte label must be truncated by the select, never widen the card or scroll the page at 360px.
- **Try again:** `.btn secondary`, ≥ 46px block size, on its own line below the text at 360px. The text wraps and is
  never clipped, including in German (+30%).
- **CSS:** new CSS (`.ps-ask-msg`, loading delay) uses logical properties only (`margin-block-start`, `text-align: start`).
  No `left`/`right` (guard-rtl-logical-css).
- **Keyboards:** a select opens no keyboard. The depends-on text fields keep their current `inputmode`, and on `change`
  the OSK closes normally. No `autofocus`; nothing focuses on load or when an answer lands.
- **Touch scrolling:** the page must still scroll by dragging over the select and over the message. Only native
  `change`/`click` listeners are added (no `pointer*`).
- **Open native picker:** replacing the select while its native picker is open closes the picker on Android/Chromium.
  This is an accepted residual risk because the loading line warns, and A6 keeps the pick if one was made. Test it on
  real touch hardware; if it can't be tested there, say so.

## 4. Accessibility
- Label association is unchanged: `<label>` wraps the `<select>`. The swap replaces both together. `title=key` stays.
- **Live region:** A5. One polite announcement per result, no announcement for loading or for a success that has no
  message, and a cache hit is silent.
- **Focus:** no jump while loading or on swap (A7). The 422 "focus first bad field" handler must ignore fragment swaps.
  It already filters on `#ps-form`; keep it that way.
- **Keyboard path:** Tab reaches select → Try again → next field. Retry also works with Enter/Space. The focus ring is
  the existing `select:focus` / `.btn` style (3:1 on all themes).
- `aria-invalid` + `aria-describedby` work on the asked select exactly as on static choices when save refuses it.

## 5. §7 gate checklist (Tester + UX verify after Dev; each needs evidence)
1. [ ] Surfaces checked: till admin **kiosk mode** (`UT_KIOSK`) and **windowed** at 1024×600, desktop, phone at 360px
   (en and `fa` RTL). Screenshots of S2, S4 and S5 exist and were looked at (no overlap, clipping or off-edge Try again).
2. [ ] GET HTML contains the stored value selected and an empty `#ps-opts-msg-KEY[role=status]`. The page returns
   before the plugin answers (fake plugin sleeps 6 s; GET ≤ 1 s).
3. [ ] S1: during a slow ask the select is enabled and `aria-busy="true"`. "Loading choices…" appears after ~400 ms and
   never on a cached answer (second load with the same values shows no flash).
4. [ ] S2: options in answer order. `<b>Big</b>` renders as text. Options carry `dir="auto"`.
5. [ ] S3 shows the empty hint without Try again. A declined/no-hook plugin shows the hint, not the failure text.
6. [ ] S4 (timeout, malformed 33 options): failure text + Try again within ~5 s, stored value selected, Save succeeds.
   Try again sends a new ask (not cached) and focus lands on the select when the button disappears.
7. [ ] S5: a stored value missing from the answer stays selected and first, and the saved-missing text shows.
8. [ ] S6: changing provider (no save) refreshes the model list once. Typing in a depends-on text field asks on blur,
   not per keystroke. Changing the asked select itself asks nothing.
9. [ ] A6: picking a model while an ask is in flight keeps that pick after the answer lands.
10. [ ] S7/S8: a hidden asked field sends no ask until shown. After Save the list is re-asked. A 422 keeps the pick.
11. [ ] Screen reader (or accessibility tree): the message container is present before the text arrives. Failure and
    hints are announced once, loading is not. Select `aria-describedby` includes the message id.
12. [ ] No focus change and no OSK opening on load or on answer. Tab order: select → Try again → next field.
13. [ ] 360px `fa`: no horizontal page scroll with a 128-byte label (`scrollWidth ≤ clientWidth`). Try again ≥ 46px
    high, full text visible. The German locale (or `en` text +30%) wraps cleanly.
14. [ ] Touch: drag-scrolling over the select and the message still scrolls the page. Picker-open-during-swap tried
    **on real touch hardware**, or reported as emulated only.
15. [ ] No `showModal`, no inline handlers, no `hx-on`. New CSS is logical. All five keys are in `en.json` with meta.
    Help `web/help/en/plugins.md` mentions loading, the empty hint ("save first") and Try again.
16. [ ] No-JS: the form still shows the stored + static options and saves.
