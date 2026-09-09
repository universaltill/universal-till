# 2026-09-09 — Shop-wide modifiers browse screen, closing ut-docs#1899

## Context

The card as filed said "modifiers have no admin UI." That premise was
**wrong** — a full per-item modifier admin UI (create/edit/deactivate
groups and options, from the item detail panel) already shipped
2026-07-24 (`docs/code-reviews/2026-07-24-item-modifiers-admin-ui.md`).
Verified on `main` before writing any code: `internal/pages/catalog/
handlers.go` already has `POST /api/catalog/modifier-group` and
`/modifier-option`, and `web/ui/partials/catalog_variants.html` already
renders the full editor inside the item detail panel.

The real, remaining gap, found by reading the schema
(`item_modifier_groups.item_id` is a single FK — a group belongs to
exactly one item, no reuse across items) and the code: there was no way
to **browse every modifier group across the whole shop at once** without
opening each item's detail panel individually. That's what this change
adds: a read-only `GET /modifiers` screen, reachable from a new button on
the Catalog page.

## Design

- `ModifierRepo.ListShopModifierGroups` (`internal/data/modifier_repo.go`)
  — the same two-query (groups, then options) pattern as the existing
  `listGroupsForItem`, generalized across the whole shop and carrying the
  owning item's name (`ModifierGroup.ItemName`, populated only by this
  query — every other query already knows which item it's scoped to).
- `GET /modifiers` (`internal/pages/catalog/handlers.go`, inside the
  existing catalog `Register`) renders `web/ui/pages/modifiers.html` — a
  plain card-per-group list with empty and error states, no new
  mechanism (`httpx.Render`, `{{ T }}`, the existing `money` template
  func).
- A button next to the existing Import/Tax codes links on `/catalog`
  is the only new discoverability surface — no new top-level nav tile,
  deliberately: `ut-docs#1897` (in review at claim time, PR #977, a
  different lane) already reserves a "Modifiers" row in a forthcoming
  `/items` section list with `Href: ""` and an explicit comment naming
  this card as the follow-up that fills it in. Adding a competing
  top-level tile here would either duplicate that entry point or conflict
  with it on merge. This card's own close-out files a Backlog follow-up
  to wire that section's `Href` once #977 lands (see below).

## Independent review (Opus, isolated worktree, fresh context)

One blocking finding, fixed and re-verified; several non-blocking notes,
most fixed same-session.

**Fixed — BLOCKER: deactivated items' modifier groups stayed visible,
unreachable and unremovable.** `DeactivateItem` never touches
`item_modifier_groups.is_active`, and `ListItems` already filters
deactivated items off `/catalog` (the only place a group can be edited or
deactivated) — so the shop-wide query, which joined `items` only for the
name and had no `items.is_active` filter, would show a deactivated item's
group forever, with no way for the merchant to act on it. Reviewer proved
it empirically against a throwaway probe before reporting it. Fixed by
adding `AND i.is_active = 1` to both the group and option queries,
matching the repo's own convention for every other item-joining browse
query (`ListItems`, `ItemsWithoutBarcode`, `ListActiveVariants`).
Re-verified with a real revert→fail→restore→pass TDD cycle, not taken on
report alone: reverted the filter, ran the new regression test
(`TestModifierRepo_ListShopModifierGroups_SkipsGroupsOfDeactivatedItem`),
confirmed it failed with exactly the reported symptom (2 groups returned
instead of 1, the deactivated item's group present), restored the fix,
confirmed green.

**Fixed — layout: a permanently empty 360px column at kiosk width.**
`modifiers.html` reused `.catalog-layout`/`.catalog-list` (designed for a
list + a 360px detail panel, which this screen doesn't have), reserving
~35% of a 1024px kiosk screen's width for nothing. Replaced with a plain
vertical flex stack, matching the pattern the reviewer's own citation
(`items.html`'s section list, from the sibling `ut-docs#1897` PR) uses
for the same "just a list, no second column" shape.

**Fixed — naming split: the button said "Modifiers," the destination
screen it edits calls the same concept "Customization options."** Every
existing operator-facing string in this repo (`catalog.modifiers.title`,
`.none`, `modifiers.none_available`) already says "customization
options" — ar/fa/tr's new translations had already, independently,
picked wording close to that existing term; only the English string
introduced a second name for one concept. Renamed `modifiers.title` (and
the button/page-`<title>` text it drives) to "Customization options" in
all four locales, exactly matching `catalog.modifiers.title`'s existing
wording, and adjusted `modifiers.subtitle`/`.empty`/`.error.server` to
match.

**Fixed — help prose named the wrong location.** `web/help/*/catalog.md`
said the button sits "next to Import"; it actually renders after Tax
codes (Import, Tax codes, Modifiers). Reworded to "on the Catalog page"
in all four locales — accurate regardless of button order, so a future
reorder can't make this go false again silently.

**Fixed — Turkish orthography.** `"Katalogu aç"` / `"Katalogunuzdaki"`
were missing the k→ğ softening every other Turkish string using this
word already has (`"Kataloğu görüntüle"`, `"Kataloğa git"`,
`"Kataloğunuzun"`). Corrected to `"Kataloğu aç"` / `"Kataloğunuzdaki"`.

**Accepted, no code change — `ItemName` on the shared `ModifierGroup`
struct.** Reviewer traced every consumer: no JSON marshalling anywhere
(HTML partials only), and the two existing templates that pass a
top-level `ItemName` alongside a `range .Groups` never reference
`.ItemName` *inside* that range today, so nothing silently renders
empty right now. Flagged as a latent trap for a future edit rather than
a live bug — the doc comment on the field already exists as the
mitigation; a separate page view-struct would remove the footgun
entirely but is more churn than this card's scope justifies.

**Filed as new Backlog cards rather than widening this diff:**
- Link the item name on each `/modifiers` row back to `/catalog` (ideally
  with the row preselected) — read-only browsing was the stated scope,
  but "found a wrong group, now has to search Catalog by hand for the
  item" is a real follow-up friction the reviewer named.
- Wire `ut-docs#1897`'s forthcoming `/items` section list's "Modifiers"
  row (currently `Href: ""`, "coming soon") to `/modifiers` once PR #977
  merges — a one-line follow-up on that IA once it lands.

## Verified beyond automated tests

`gofmt -l .` clean. `go build ./...`, `go vet ./...` clean.
`golangci-lint run ./...`: 0 issues. Full `go test ./...`: all packages
green (post-fix). Guards: `guard-data-access.sh`, `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh` (screenshots regenerated
twice — once for the initial diff, once after the post-review copy/
layout fixes — via `make docs-shots` against the pre-installed
Chromium), `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-page-http-error.sh`, `guard-compliance-claims.sh`,
`guard-htmx-loaded.sh`, `guard-emoji-font.sh` all clean.

Live-driven, twice (before and after the review fixes), against a real
built binary (`go run .`, scratch SQLite DB, real HTTP requests through
`curl`/Playwright, not just rendered-HTML assertions):
- Empty-shop state renders the CTA.
- A created item + group + option round-trips through the real handlers
  and renders correctly on `/modifiers`, including locale-aware money
  formatting (`£0.50`).
- **The B1 fix specifically**: created two items with one group each,
  deactivated one item via the real `/api/catalog/item/deactivate`
  endpoint (not a raw DB write), confirmed `/modifiers` dropped exactly
  that item's group while keeping the other — matching the regression
  test's assertion.
- Screenshotted (and looked at, not just asserted on) at the 1024×600
  kiosk floor, 360px phone width, and `fa` (RTL) — nav rail correctly
  flips to the right, group text right-aligns, the option list's
  `padding-inline-start` correctly becomes right-side indent, and the
  money amount renders with Persian digits via the existing locale-aware
  `money` template func. No hardcoded `left`/`right` anywhere in the new
  template. Re-screenshotted after the layout fix: the dead 360px column
  is gone.
- No text inputs on this screen, so no on-screen-keyboard/`inputmode`
  concern; every control is a plain `<a>`, natively keyboard-reachable.

## Safe-to-merge verdict

Yes. One real blocker, fixed and TDD-reverified; UX/naming/help-prose
findings fixed same-session; one accepted latent-trap note requires no
code change; two real follow-ups filed as new Backlog cards rather than
widening this diff.
