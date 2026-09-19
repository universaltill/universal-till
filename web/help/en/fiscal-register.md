---
id: fiscal-register
title: Fiscal register (Germany)
section: Setting up your shop
order: 112
summary: "Record the till and TSE details Germany's §146a Abs. 4 AO till-notification needs, grouped by business location — you file the notification yourself in Mein ELSTER."
routes: [/fiscal-register]
keywords: [fiscal, germany, AO, TSE, ELSTER, kassensichv, notification, till, registration]
---

# Fiscal register (Germany)

Since 2020, German tax law (§146a Abs. 4 AO) has required every shop using an electronic till to report that till — and its TSE, the certified device that signs each sale — to their local Finanzamt. The notification itself is filed through the tax office's own **Mein ELSTER** portal, by the shop, not by Universal Till. This page exists so you have the details that filing needs already written down and organized by location, instead of hunting through paperwork when it's time to submit.

This page only records data. It does not fill in, generate, or submit anything to ELSTER on your behalf. Whether what you enter here is complete and correct for your situation is between you and your tax advisor or the Finanzamt — this page doesn't check that for you.

## How to use it

1. Open **Fiscal register** from the menu (manager only; only shown once your shop's country is set to Germany **and** the German tax plugin is installed and enabled). Entries are grouped by the business location each till belongs to, and the search box above the list filters across every group at once.
2. Tap the **+** button next to the search box to record a till/TSE pairing: pick the till (register) from the list, then fill in the system type (pre-filled with the common default, but editable), the till's software name and serial number, and the TSE's serial number, certification ID, and type. **Acquired on** is required — the date the till was obtained. **Commissioned on** (when it actually went into use) is optional. A location's heading (and its address form) only appears once it has at least one entry.
3. If a location's address isn't on file yet, fill in the small address form under that location's heading (street, postcode, city) and press **Save address**.
4. Once you've filed the notification for a till in Mein ELSTER, there's nothing further to do here — the entry simply stays on the list as your own record.
5. Tap any row to open it. An entry can't be edited once created, so what opens is a read-only view of everything you recorded, not a form — that's expected, not a bug. When a till goes out of service, press **Mark decommissioned** inside that popup. This stamps today's date and changes its status — it never removes the row. Keeping retired tills on record is the point: the obligation this page supports doesn't end when a till is replaced.

## Good to know

- A till can appear more than once here over its lifetime — for example, if its TSE is swapped after a hardware failure. Each pairing gets its own entry, and none of them are ever deleted or edited once created — tap a row any time to see its details again.
- A till with no location assigned still shows up, under its own "no location assigned" group, so nothing you've entered is ever silently hidden.
- The one-month banner is a reminder, not an enforcement mechanism, and it isn't only for new entries — it also appears for a month after you mark a till decommissioned, since removing a till needs its own notification too. Dismissing it isn't possible because there's nothing to dismiss; it simply stops showing once a month has passed since whichever date triggered it.
- The fiscal register is shop-wide and always managed from the **main till**: on a joined till, adding an entry, marking one decommissioned, or saving a location's address shows a message pointing you back to the main till, rather than accepting a change that would only apply locally. Entries added on the main till reach every joined till automatically, the same way registers and locations do.
- You can export everything on this page as a plain-text summary — organized the same way, one block per business location — from **Data management → Export** (pick the §146a Abs. 4 AO entry). It's a summary to help you fill in Mein ELSTER yourself, not a file ELSTER accepts directly.
