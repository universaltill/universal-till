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
3. Each plugin has its own settings page; some settings are shared shop-wide, others are per till.
4. An installed plugin that ships its own documentation shows a Docs button on its card, which opens that documentation right inside the till.
5. In a shop with several tills, install and remove plugins on the **main till** only: every joined till fetches the same plugin from the store itself and applies the change automatically within about half a minute. A joined till's own install, uninstall, enable/disable and update actions are refused with a message pointing to the main till. Plugins imported from a file are the exception — importing still works on any till, joined ones included, but the plugin stays only on the till it was imported to and is never copied to the others.
6. If a plugin's files go missing or fail to load — for example on a till that has just joined a shop — its card shows a red **Broken** badge. Anything that depends on that plugin fails safe: for a tax plugin, the items it taxes can't be sold until it's back. A joined till re-downloads the plugin from the store automatically within about half a minute; on the main till, reinstall it from the store.
