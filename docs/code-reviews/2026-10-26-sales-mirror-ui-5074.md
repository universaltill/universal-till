# Review: the Tills page card for the sales copy (ut-docs#5074 M5)

Part of ut-docs#5074 (design §10). Stacked on M4.

## What shipped

- A card on Settings → Tills (`web/ui/partials/tills_sales_mirror.html`,
  `GET /ui/tills/sales-mirror`, manager-only like the page):
  - main till: On/Off from the shop setting, the window, one line per
    additional till (up to date / N changes to go / not copied yet;
    satellites left out), **Turn on** / **Turn off**; with this till's own
    kill switch on it says "Off on this till" and offers no button;
  - additional till: the state of its own copy (Up to date, Catching up,
    Off, Off on this till, Paused: low disk / size limit / update this till,
    Needs a secure link, Off on the main till, Not kept on a satellite), the
    changes to go, and "Sales copied: N, since <date>".
  - The switch re-renders the card in place; a refused press answers the
    card with the reason (htmx swaps only a 2xx). Enabling an already-on copy
    changes nothing (no new epoch, no fleet re-sync).
- 23 keys `tills.sales_mirror.*` in en, ar, fa and tr with metadata
  (translated in this change; terminology matches the existing keys:
  الصندوق الأساسي / صندوق إضافي / صندوق تابع, صندوق اصلی / اضافی / وابسته,
  ana / ek / uydu kasa).
- Manual: "A copy of every sale on every till" in `multitill.md` in en, de,
  ar, fa and tr; the help-drift baseline shifted in lockstep; docs-shots run
  in Docker (`ut-docs-shots:ubuntu2404`): no screenshot pixel changed (the
  card is below the captured fold), manifest refreshed.

## Looked at

Screenshots of the card (Docker, Chromium, 1024×768): English and Persian
(RTL: heading, `?` link and button mirror correctly; wraps cleanly);
German falls back to English text (German comes from the language pack).
Not looked at: dark theme (the app theme is a setting, not the media query
the shot used), 10-inch kiosk size, the additional-till states in a browser
(covered by rendered-HTML tests).

## Independent review (Fable, strict)

1. BLOCKER — the new route was not classified for demo mode: **fixed**.
2. MAJOR — "Up to date — N changes to go" on an additional till mid
   catch-up: **fixed** (new "Catching up" state, also during a re-sync).
3. MAJOR — the main-till card followed the kill-switched gate, so "Turn on"
   showed while on and re-minted the epoch on every press: **fixed** (the card
   follows the setting; a kill switch shows "Off on this till"; enable is
   idempotent).
4. MINOR — raw ISO date: **fixed** (`datetime`, locale digits).
5. MINOR — refused presses vanished on the htmx path: **fixed**.
6. MINOR — German "Führungskraft" vs "Manager": **fixed**.
7. MINOR — the Arabic topic mixes الصندوق الأول and الأساسي: **accepted**;
   the new section and keys use الأساسي (as `sync.main_till_unreachable`
   does); unifying the older text is a follow-up.
8. MINOR — main-till lines cannot say why a till is paused (acks carry no
   reason): **accepted**, follow-up.
9. NIT — unstyled list class: **removed**.
10. NIT — tests for 403, refusal in place, satellites, every state's key,
    catching up and the killed main till: **added**.

Deferred pieces judged acceptable dark: per-till "Turn off on this till" and
"Delete this till's copy" buttons, receipts copy views, promote disclosure
(M6), nav chip. The reviewer asks that the PIN step-up for turning the copy
on lands before the copy is un-darked: the route is `sync_management`-only
today (stricter than `checkOrElevate` for a non-manager, but no fresh PIN
for a manager) — card to file.

## CI follow-up

The German render audit flagged "Off": the first English texts of the
on/off states equalled an existing catalog string, so a German page fell
back to an English word already translated elsewhere. The states now read
"Switched on" / "Switched off" / "Switched off for the shop" and the button
"Turn copying on" / "Turn copying off" (no new English text equals an
existing one), so the audit treats them as new keys awaiting the packs.

## New keys for the language packs

`tills.sales_mirror.{title, help, status_on, status_off, window,
till_waiting, till_catching_up, till_up_to_date, turn_on, turn_off,
copy_behind, copy_summary, copy_empty}` and `tills.sales_mirror.state.{up_to_date,
catching_up, off, off_here, paused_low_disk, paused_budget, needs_update,
needs_tls, main_off, satellite}`.
