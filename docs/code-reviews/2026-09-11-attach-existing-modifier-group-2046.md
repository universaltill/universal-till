# Attach an existing modifier group to another item (ut-docs#2046)

## What shipped

ADR-0090 (ut-docs#2013) landed the schema/data-layer foundation for
many-to-many modifier groups — `item_modifier_group_links`,
`ModifierRepo.LinkGroupToItem`/`UnlinkGroupFromItem` — but wired up no
handler or UI. This change is the write path:

- `internal/data/modifier_repo.go`: `ListAttachableModifierGroups` (active
  groups not yet linked to an item), `GroupLinkCount`,
  `NextGroupSortOrderForItem`, and `UnlinkGroupFromItemUnlessLastLink` (an
  atomic conditional delete — see "What the independent review found").
- `internal/pages/catalog/handlers.go`: `POST
  /api/catalog/modifier-group/attach` and `.../detach`.
- `web/ui/partials/modifier_group_admin.html`: an "Attach existing group"
  picker (hidden when nothing is attachable) and a "Remove from this item"
  button on each already-linked group.
- i18n keys added to this repo's 4 shipped locales (ar/en/fa/tr); `de`/`es`
  live in `ut-plugin-language-{de,es}` and are NOT touched here —
  `en.json` changed, so `lang-pack-drift` will (correctly) go red on
  `main` until a follow-up PR lands new keys there. Same lane, same
  cycle, per `scrum-master`'s "work that has no card is not covered" rule.
- `web/help/*/catalog.md` (all 5 locales) documents the new capability.

**Architect decision, recorded here** (the issue's own acceptance
criteria asked for this call): `DeleteGroup` (hard delete, removes a
group from every linked item) stays unwired to any handler — unchanged
from before this card. This card ships attach + detach-from-one-item
only; a merchant who wants a group gone from sale entirely already has
the group's own "Active" checkbox. A future card can revisit
hard-delete-everywhere UI if a real shop asks for it.

**Second decision**: detaching a group's LAST remaining link is refused
(409), not silently allowed. `UnlinkGroupFromItem`'s own (now corrected)
doc comment used to claim an orphaned group "stays manageable via
/modifiers" — false: every list query (`ListShopModifierGroups`/
`ListAllShopModifierGroups`, and so both `/modifiers` and the item-scoped
panel) only ever surfaces a group THROUGH a link row, so a zero-link group
would become permanently unreachable, not merely unattached. This is a
new product rule; it isn't in an ADR, only in code comments + this
record — noted as a gap the independent review flagged (see below),
accepted rather than fixed given this is a UI-level guard, not an
architectural schema decision.

## What the independent review found (Opus, cold read of the diff)

Full findings ranked high→low; **fixed** unless noted:

1. **HIGH — the 409 refusal was invisible to the merchant.** The original
   detach guard answered through `common.LocalizedError`, i.e. a plain
   `http.Error` body (`text/plain`). `app.js`'s `htmx:beforeSwap` only
   force-swaps a non-2xx **text/html** body (its own `ut-docs#916`
   comment), and outside the sale screen there is no `#pos-alert`
   fallback — so on both `/modifiers` and the item dialog, clicking
   "Remove from this item" on a group's last link would silently do
   nothing. **Fixed**: `renderModifierMutationResult` now takes a
   `status`/`notice` pair; a refusal answers through the SAME fragment
   the mutation always re-renders (text/html, the button's own
   hx-target), with a `role="alert"` notice rendered inline. Verified
   both by a Go test asserting `Content-Type: text/html` +
   `role="alert"` in the body, and by driving the real app in a browser
   (screenshot: the refusal now shows "This is the only item using this
   group — deactivate it instead if you no longer need it." in red at
   the top of the page, group intact).
2. **HIGH — help docs claimed the button is hidden; it never was.** All 5
   locale `catalog.md` files said "that button is unavailable when it's
   the group's last remaining item." The button is always rendered; only
   the server-side action is refused. **Fixed**: reworded to "removing it
   is refused when..." in all 5 locales — now true, and coherent with
   finding #1's fix (the refusal is visible, so "always present, refused
   with an explanation" is an honest, complete description).
3. **MEDIUM — TOCTOU race in the last-link guard.** The original handler
   did `GroupLinkCount` then `UnlinkGroupFromItem` as two separate calls;
   two concurrent detaches against a 2-link group's own two different
   items could both read `links==2`, both pass, both delete, orphaning
   the group. **Fixed**: `UnlinkGroupFromItemUnlessLastLink` is one
   atomic conditional `DELETE ... AND (SELECT COUNT(*) ... ) > 1`,
   `RowsAffected()==0` means refused. Verified with a real concurrent
   test (`TestModifierRepo_UnlinkGroupFromItemUnlessLastLink_ConcurrentRaceLeavesOneLink`,
   run under `-race`): two goroutines racing to unlink both items of a
   2-link group, exactly one succeeds, the group never drops to 0 links.
4. **MEDIUM — sortOrder computed wrong.** The attach handler used
   `len(existing groups)` as the new link's `sort_order`. Per-item
   `sort_order` can go sparse after a detach (e.g. an item keeps groups
   at 0 and 2 after its middle one is removed); `len()==2` collides with
   the surviving sort_order-2 row, and `ORDER BY sort_order, name` falls
   back to alphabetical instead of "appended last" as intended.
   **Fixed**: `NextGroupSortOrderForItem` computes
   `COALESCE(MAX(sort_order)+1, 0)` per item. Regression test
   (`TestModifierRepo_NextGroupSortOrderForItem_HandlesSparseOrder`)
   pins the sparse case directly (create 3, detach the middle one, assert
   the next slot is 3, not 2).
5. **MEDIUM — N+1 on `/modifiers`.** The shop-wide picker originally
   called `ListAttachableModifierGroups` once per distinct item inside
   the render loop — 200 modifier-bearing items = 200 extra queries.
   **Fixed**: `distinctActiveModifierGroups` computes the shop-wide
   distinct-active-group set ONCE in Go from the slice
   `ListAllShopModifierGroups` already fetched, then filters per item
   with no further SQL — zero extra queries for the whole page. The
   item-scoped dialog (one item, no shop-wide slice in scope) still pays
   exactly one `ListAttachableModifierGroups` query, which is not N+1.
6. **LOW — attach didn't validate attachability server-side.** A stale
   picker (open since before another tab deactivated the group, or
   attached it to this same item) could submit a `groupId` the picker's
   own invariant no longer allows — an inactive group, or one already
   linked (silently re-ordering it via `LinkGroupToItem`'s
   `ON CONFLICT DO UPDATE`). **Fixed**: the attach handler now
   re-validates `groupId` against a fresh `ListAttachableModifierGroups`
   call before linking; a stale submission gets the existing
   `catalog.error.invalid_request` 409 (also now visible, via the same
   fix as #1). Regression test:
   `TestModifierGroupAttach_RefusesStalePickerSubmission` covers both the
   inactive-group and already-linked cases, including asserting the
   existing link's `sort_order` is untouched.
7. **LOW — stale doc comments.** `LinkGroupToItem`/`UnlinkGroupFromItem`'s
   comments said "no handler calls this yet" / "stays manageable via
   /modifiers" — both now false. **Fixed**: both comments corrected to
   describe the actual, now-wired behavior and point at the handlers/
   guard that call them.
8. **LOW — logging convention, checked and dismissed.** Flagged as
   inconsistent with `internal/pages/common`'s `internal/logging`
   convention; verified this exact file (`internal/pages/catalog/
   handlers.go`) already exclusively uses stdlib `log.Printf` throughout
   (e.g. the pre-existing `writeRowOOB` closure) — the new code matches
   its own file's real precedent. No change; the reviewer's citation was
   for a different package's own doc comment, not this one's actual
   practice.

**Also independently re-verified**: the two pre-existing tests this diff
updates (not just adds) genuinely reflect new, correct behavior rather
than being weakened to force a pass — `modifiers_relocate_test.go`'s
assertion changed from "must not contain itm2's group name at all" to
"must not appear as itm2's own linked/editable row (`value="Milk"`)
**but must** appear as an attach-picker option (`>Milk<`)" — a strictly
stronger check (the old negative alone would no longer catch a real
cross-item leak into the wrong section, since "Milk" legitimately
appears somewhere on the page now).

## What was verified beyond automated tests

- `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...` (0 issues),
  full `go test ./...` — all clean.
- `guard-i18n.sh`, `guard-help-drift.sh`, `guard-help-topics.sh`,
  `guard-data-access.sh`, `guard-page-http-error.sh` — all pass (the
  help-drift baseline for the pre-existing, unrelated `catalog` topic
  drift (ut-docs#329) was updated deliberately: both sides' `top_bullets`
  shifted by the same +1 when a bullet was added to all 5 locale files,
  same known gap, not newly introduced).
- Drove the real app in a browser (Playwright against the compiled
  binary) at kiosk width (1024×600), phone width (360px), and in Farsi
  (RTL): attach end-to-end (item with zero groups → attach existing →
  fully editable with its options), detach end-to-end, the last-link
  refusal's confirm dialog and (post-fix) its visible notice, all
  screenshotted and looked at — no clipping, no overlap, RTL renders
  correctly, touch targets consistent with the existing form controls.

## Safe-to-merge verdict

Safe to merge. All independent-review findings addressed except #8
(checked, not applicable) and the "last-link refusal isn't in an ADR"
gap noted above, which is accepted as proportionate for a UI-level guard
rather than an architectural schema change.

## Explicitly deferred (new Backlog cards, not fixed here)

- `ut-plugin-language-de`/`ut-plugin-language-es`: add the 5 new
  `catalog.modifiers.*` keys — same lane, same cycle, tracked as the
  standing "core key added → pack follow-up" rule, not a separate ticket.
- Hard-delete-everywhere UI for a modifier group (`DeleteGroup`): out of
  scope per the recorded Architect decision above; revisit only if a real
  shop asks.
