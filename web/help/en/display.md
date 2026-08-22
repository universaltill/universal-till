---
id: display
title: Languages & display
section: Setting up your shop
order: 140
summary: The whole till speaks English, Türkçe, فارسی and العربية — including right-to-left layout — and adapts to touch screens with an on-screen keyboard and adjustable text size.
routes: [/settings]
keywords: [theme, scale, kiosk, screen, osk, keyboard]
---

# Languages & display

The whole till speaks English, Türkçe, فارسی and العربية — including right-to-left layout — and adapts to touch screens with an on-screen keyboard and adjustable text size.

## How to use it

1. Change the language from the menu; each user can pick their own.
2. Settings → Display: adjust the UI scale for your screen.
3. The on-screen keyboard pops up automatically on touch screens (or force it on/off in Display settings).
4. Settings → Tills: give this till its own name (e.g. "Front Counter") so it's easy to tell apart from other tills — shown on the Tills page and defaults to "Till 1" until you set one.
5. Settings → Tills: "This till's register" tells the till which register it is. With a single register it's picked automatically; on a shop with more than one register, choose it here — cash payouts (like a bottle-deposit refund) are recorded against this register's open shift, and the till will refuse to record a payout until it knows which register it is.
6. Settings → Shop type: the kind of business you picked in the setup wizard (café, retail, service trade, hospitality, market stall or other) — change it here any time.
7. Settings → Data → Sample data: if you loaded the starter catalogue during setup, remove it all here with one tap — sample items, the 3 sample customers, and the 3 sample promo codes together, so a discount code from the demo data doesn't stay redeemable after you've moved on. Anything already in real use is kept (your sales history stays intact, and a sample customer or promo you've actually started using survives) — the till tells you how many it removed and how many it kept. If a sample item or customer is currently in the till's basket (cashier or self-order), removal is blocked until the basket is cleared — clear it and try again.
8. Settings → Display also has a window-mode selector (normal, maximized, fullscreen, kiosk) and a launch-on-startup toggle. On Linux desktop, both are real: pick a window mode or toggle autostart, and it applies the next time you start the till — fullscreen/kiosk hide the OS's own window chrome, and launch-on-startup adds or removes a real autostart entry. On a Raspberry Pi kiosk till, the window-mode selector is real too and applies immediately, no restart needed: switch it to "kiosk" and the dedicated kiosk screen turns on right away; switch it to anything else and it turns off (the Pi's launch-on-startup toggle stays scaffolding — the box already starts the kiosk screen on its own from boot). On a Pi appliance the kiosk screen is the only screen — switching away from "kiosk" closes it and there's no console behind it, so you'll need another device on the same network (or SSH) to switch it back. On macOS and Windows, both settings are still scaffolding: the till saves your choice, but nothing changes until that platform's support ships — restart the till to pick up a different window mode there in the meantime. The "Exit to OS window" action next to them still asks for a manager PIN but doesn't yet do anything on any platform.
9. Picking a country in the setup wizard automatically fetches that country's free plugins (today: the matching language pack) in the background — no marketplace hunting required, and it works even if you're offline at the time, retrying on its own once the network's back. While one is still installing (or waiting to retry), Settings → Data shows a small note with a Remove button if you'd rather not have it — nothing is locked in.
