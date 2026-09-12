---
id: plugins
title: Plugin store
section: Connecting & extending
order: 330
summary: "Add features without changing the core app: payments, themes, language packs, integrations, AI tools and more."
routes: [/plugins, /plugins/store, /plugins/{id}/settings]
keywords: [plugin, marketplace, install, extension, theme]
---

# Plugin store

Add features without changing the core app: payments, themes, language packs, integrations, AI tools and more. Every plugin is signed and verified before it runs.

## How to use it

1. Open Plugins → Store to browse the catalog.
2. Install with one click; plugins carry trust badges (gold = official Universal Till, green = verified developer) and unverified publishers ask for your confirmation first.
3. A plugin marked **Paid** needs an entitlement your merchant manager approves in the marketplace portal before it can be installed — trying to download one without approval shows a message explaining that, instead of downloading.
4. Each plugin has its own settings page; some settings are shared shop-wide, others are per till. A tax plugin that supports takeaway rate overrides shows a dedicated editor here instead of raw text: enter the takeaway percentage next to each tax code, and any item using that code switches to that rate when the order is takeaway. Leave a field blank to charge the dine-in rate. The AI Assistant plugin's settings page also shows a notice above its API-key field: choosing a hosted provider (provider = claude or openai) sends what the AI features work on to that provider's servers, while the default self-hosted option keeps everything on your own hardware — read it before entering a key.
5. An installed plugin that ships its own documentation shows a Docs button on its card, which opens that documentation right inside the till.
6. **Import from file** expects a bundle that was signed by the marketplace — the two ways to get one are downloading it from the Plugin Store (even if you then transfer the file by hand) or using **Export** on a till that already has the plugin installed. A `.tar.gz` attached directly to a plugin's GitHub release page is not signed and will be refused with a message saying so — that download exists for developers inspecting the source, not for installing on a production till.
7. In a shop with several tills, install and remove plugins on the **main till** only: every joined till fetches the same plugin from the store itself and applies the change automatically within about half a minute. A joined till's own install, uninstall, enable/disable and update actions are refused with a message pointing to the main till. Plugins imported from a file are the exception — importing still works on any till, joined ones included, but the plugin stays only on the till it was imported to and is never copied to the others.
8. If a plugin shows a red **Broken ⚠** badge on the Plugins page, its files are missing or unreadable on that till (this can happen right after a till joins the shop). How it recovers depends on where it came from: a plugin installed from the store is reinstalled automatically within about half a minute — no action needed. A plugin imported from a file has no store listing to re-fetch, so it is **not** repaired automatically — import the plugin file again on that till to fix it. Until the plugin recovers, items whose tax rate it decides can't be sold on that till: when only one item in the basket is affected, the sale screen names it and removing it lets the rest of the sale complete; when every item is affected (for example when the broken plugin is the till's only tax plugin), checkout on that till is unavailable until the plugin recovers — wait a moment and try again.
9. A plugin you download from the store but don't install right away stays staged for 48 hours; if that window passes, its **Downloaded** badge quietly reverts to a plain **Download** button and it needs downloading again before it can be installed. This is routine disk housekeeping, not an error — nothing is lost, since you can just download it again.
10. The till checks for plugin updates in the background — you don't need to open this page for an update to be found. **Language packs update themselves automatically**, so a newer translation reaches your till without any action from you. Every other kind of plugin waits for you to review it here: a green **Plugin updates available** chip appears in the status bar at the bottom of the screen whenever one is pending, with a count — tap it to come straight to this page and update from the list.
11. A plugin the till can't match against the marketplace catalog — most commonly one installed via **Import from file** — shows an **Update status unknown** chip instead of an update badge, so it never looks like it's already current when nobody actually checked. This is informational only; it doesn't block anything.
