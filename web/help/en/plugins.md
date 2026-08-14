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
4. Each plugin has its own settings page; some settings are shared shop-wide, others are per till.
5. An installed plugin that ships its own documentation shows a Docs button on its card, which opens that documentation right inside the till.
6. In a shop with several tills, install and remove plugins on the **main till** only: every joined till fetches the same plugin from the store itself and applies the change automatically within about half a minute. A joined till's own install, uninstall, enable/disable and update actions are refused with a message pointing to the main till. Plugins imported from a file are the exception — importing still works on any till, joined ones included, but the plugin stays only on the till it was imported to and is never copied to the others.
7. If a plugin shows a red **Broken ⚠** badge on the Plugins page, its files are missing or unreadable on that till (this can happen right after a till joins the shop). How it recovers depends on where it came from: a plugin installed from the store is reinstalled automatically within about half a minute — no action needed. A plugin imported from a file has no store listing to re-fetch, so it is **not** repaired automatically — import the plugin file again on that till to fix it. Until the plugin recovers, items whose tax rate it decides can't be sold on that till: when only one item in the basket is affected, the sale screen names it and removing it lets the rest of the sale complete; when every item is affected (for example when the broken plugin is the till's only tax plugin), checkout on that till is unavailable until the plugin recovers — wait a moment and try again.
