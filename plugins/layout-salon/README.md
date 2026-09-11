# ut-plugin-layout-salon — a hair salon / barber shop Menu

The first `layout` plugin (ADR-0088, ut-docs#1904): it changes **what the
Menu screen, the /items screen's own section list and the nav rail show**,
and nothing else. A salon does not seat tables or route tickets to a
kitchen, and it sells *services* — but a service is still an item,
"short/long" is still an option set, and a service is a stock-untracked
inventory row
(ut-docs#1843/#1850). **The catalog, variant and inventory concepts do not
change; only the showing method does** — the product owner's constraint on
ut-docs#1904, and the reason this is a presentation manifest rather than a
code fork.

It lives in this repository, beside `plugins/tax-tr`, so it is installed
and exercised in CI against the real till
(`internal/plugins/layout_validation_test.go`,
`TestLayoutSalonPlugin_InstallsAndDeclaresItsAmendments`); it moves to
`universaltill/ut-plugin-layout-salon` when first published to the
marketplace (ADR-0009 naming).

## What it does

`plugin.json` carries three `layout` entries — one amendment document per
slot (ADR-0088; the `items` slot generalized by ut-docs#1911, the `rail`
slot by ut-docs#1912):

**`menu` slot:**

| Menu key | Amendment | Effect |
|---|---|---|
| `/tables` | `hide` | The Tables tile leaves the Menu. The page stays reachable at `/tables`. |
| `/kitchen-stations` | `hide` | Same — tile gone, route open. |
| `/items` | `label_key`, `icon`, `order` | "Items" reads **Services**, with a scissors icon, listed first. |

Hiding removes the **tile only** (ADR-0088 Decision D). A merchant who
still needs Tables finds it under *Settings → Hidden menu tiles*, which
names this plugin and restores the tile without uninstalling anything.

**`items` slot** (the /items screen's own left-rail section list):

| Items-rail key | Amendment | Effect |
|---|---|---|
| `/catalog` | `label_key`, `order` | The "Library" row reads **Services** too (reusing the same `layout.salon.services` key the Menu tile uses), listed first. |

No hide mechanism exists for this slot yet — unlike the Menu, the /items
rail has no "restore" recovery surface today (ut-docs#1911 flags this as a
deliberate, deferred gap), so this plugin only relabels/reorders here,
never hides.

**`rail` slot** (the nav rail's primary links — Sell, Menu, Inventory,
Orders — rendered on every page):

| Rail key | Amendment | Effect |
|---|---|---|
| `/orders` | `order` | Orders moves ahead of Inventory: Sell, Menu, **Orders**, Inventory. A salon's queue of in-progress services is tapped far more often than its retail stock. |

A reorder only — it introduces no locale key, so there is nothing to
translate. Same deferral as the `items` slot: no hide here either. And
Sell (`/`) and Menu (`/menu`) are **protected** on this slot (ADR-0088
Decision J): they are the only two rail entries with no equivalent tile
anywhere on the Menu, so a plugin may reorder them but never hide or
re-label them.

## What it cannot do — and why

- It cannot hide `/fiscal-register`, `/fiscal-device`, `/journal`,
  `/settings`, `/plugins`, `/help` or `/report-issue` on the Menu
  (Decision E), and cannot hide or re-label `/` or `/menu` on the rail
  (Decision J). A manifest that tries is refused at install, naming the
  key.
- It cannot give a tile a *literal* label: `label_key` is a locale key,
  shipped in this plugin's own `locales/<locale>.json` files for every core
  locale (Decision G). If a locale is missing the key, the till falls back
  to the core label ("Items"), never to the raw key.
- It cannot bring an icon *file*: `icon` names an entry in core's built-in
  icon set (`internal/httpx/icons.go`). An unknown name falls back to the
  core tile's icon (Decision H).
- It cannot restructure a key another installed `layout` plugin already
  restructures — the second install is refused naming the first (Decision
  F). Two plugins *hiding* the same key is fine.

## Packaging

`runtime: "none"` — asset-only, like a theme or a language pack: no
executable, no permissions requested, nothing runs. The bundle is this
directory as-is (`plugin.json` + `locales/`).
