---
id: translate-mode
title: Translate mode
section: Setting up your shop
order: 151
summary: Managers can see which text key each label comes from, open it in Weblate, and change the wording for this shop.
keywords: [translate mode, translation, weblate, language, wording]
---

# Translate mode

Translate mode shows where each text on the till comes from, so a translator can fix it at the source and a manager can reword it for this shop. Only managers can use it, and it is never available on a self-order kiosk or in the public demo.

## Turning it on

1. Open **Menu → Translations**.
2. Press **Turn on translate mode**. The page reloads and the status bar shows **Translate mode**.
3. To leave it, press **Turn off** in the status bar.

Translate mode stays on for this browser until you turn it off. If someone without the manager permission signs in, the till shows normal pages.

## Opening a text

- With a mouse: hold **Alt** and click the text. Some Linux desktops use Alt+click to move windows; if nothing opens, use a long press or the keyboard instead.
- On a touchscreen: press and hold the text for about half a second. In translate mode a hold opens the panel instead of selecting the text, and a held sale button does not start rearranging the buttons.
- With a keyboard: move the focus to a button, link or field and press **Alt+Enter**. Plain text such as a heading cannot take the focus; use Alt+click or a long press for it.

A panel opens at the side of the screen. The page stays usable, so you can open the next text while the panel is open.

## What the panel shows

- **Key**, **area**, **description**, **placeholders** and **length limit**: what translators see for this text.
- **Open in Weblate**: opens this text in the shared translation service, in the language the till is showing. The QR code opens the same link on a phone or another computer. The till only shows the link; it never connects to Weblate itself.
- **Shop override**: changes the wording on this till only, like the translation editor. Reset goes back to the built-in text. Reload the page to see the change.
- **Areas on this page**: the groups in the translation service that the texts on this page belong to. Invite a translator to these areas to cover the whole page.

A text that comes from a plugin has no Weblate link: it is translated with that plugin.
