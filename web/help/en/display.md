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
7. Settings → Data → Sample data: if you loaded the starter catalogue during setup, remove it all here with one tap — sample items, the 3 sample customers, and the 3 sample promo codes together, so a discount code from the demo data doesn't stay redeemable after you've moved on. Anything already in real use is kept (your sales history stays intact, and a sample customer or promo you've actually started using survives) — the till tells you how many it removed and how many it kept.
8. Settings → Display also has a window-mode selector (normal, maximized, fullscreen, kiosk) and a launch-on-startup toggle — these are scaffolding for now: the till stores the choice, but it only takes effect once this platform's window-mode/autostart support ships. The "Exit to OS window" action next to them asks for a manager PIN and calls the same not-yet-wired platform hook.
