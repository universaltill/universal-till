# Code review — Option sets: till parity, directives, cloud wiring (ut-docs#3319)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3319 (`complexity:hard`, cross-repo: this repo +
  `ut-cloud` + `ut-my-shop`), child of epic #2493 (my./till parity).
- **Branch:** `option-sets-manage-parity-3319`
- **Author:** Opus 5.5 subagent, `complexity:hard` per `MODEL-ROUTING.md`.
- **Reviewer:** independent pass, Fable subagent in this repo's own
  checkout (no visibility into the implementation reasoning).
- **Verdict: SAFE TO MERGE**, after fixing the one blocker and the
  should-fix findings the review turned up.

## The gap

The till already had shop-wide option sets (ut-docs#1900, migration 020):
create a set, add values, apply to an item, generate variants
(`internal/data/option_set_repo.go`). It had **no rename, no value
edit/reorder/remove, no active-flag toggle, and no delete** — not even on
the till's own local `/catalog/option-sets` screen, let alone from my.
(Manage-shop). This card closes that gap on both surfaces plus the cloud
directive pair that lets my. drive it, mirroring the existing
`save_modifier_group`/`delete_modifier_group` wiring (ADR-0101/ADR-0095).

## What shipped (this repo's slice)

- **New `internal/data/option_set_save_repo.go`**: `SaveOptionSet` (one
  write path serving both the till's local screen and the cloud
  directive, same double duty as `ModifierRepo.SaveGroup`), thin wrappers
  `RenameOptionSet`/`ReplaceOptionSetValues`/`SetOptionSetActive`,
  `DeleteOptionSetIfUnused` (refuses when `item_option_sets` has any row
  for the set — checked explicitly in the same transaction, since both
  relevant FKs are `ON DELETE CASCADE` and would otherwise silently
  succeed), `ItemsUsingOptionSet`, `ListOptionSetsWithItems`. New
  sentinels `ErrOptionSetNotFound`/`ErrOptionSetInUse`/
  `ErrOptionSetValueInvalid`/`ErrOptionSetTooManyValues`. Bounds (100
  values, 128 runes each) match the modifier-group payload's magnitude.
- **Till-local UI** (`internal/pages/catalog/handlers.go`,
  `web/ui/pages/option_sets.html`): new routes for rename, per-value
  edit/move/remove, active toggle, delete. Reorder is explicit ↑/↓
  buttons — deliberately **not** drag, since this screen can run on the
  kiosk touchscreen and a new pointerdown/drag handler risks eating the
  page's scroll gesture (precedent: the floor-plan editor). "Used by"
  renders inline on the card, visible without clicking anything, so the
  disabled Delete button explains itself.
- **Cloud directive wiring**: `save_option_set`/`delete_option_set` added
  to `mainTillOnlyTypes`/`catalogTypes` (`internal/cloudsync/catalog_directives.go`),
  `decodeSaveOptionSet` mirroring `decodeSaveModifierGroup`'s shape
  (`values` is a JSON-encoded array **string**, same convention as
  `options`), dispatch cases in `cloudsync.go`, `Hooks.SaveOptionSet`/
  `DeleteOptionSet` wired in `internal/pages/cloudsync_wire.go` /
  `cloudsync_catalog_wire.go`, and a new `remoteOptionSetsReport` feeding
  `DeviceExtra`'s periodic snapshot (mirrors `remoteModifierGroupsReport`'s
  shape exactly: id/name/active/values/items(capped)/items_total).
- i18n: 20 new `catalog.option_sets.*` keys in every core locale
  (en/ar/fa/tr); help topic `web/help/*/option-sets.md` updated in every
  locale with a new "Changing or deleting a set" section;
  `web/help/img/manifest.json` + the four `option-sets.png` screenshots
  regenerated for real via `make docs-shots` (see finding 1).
- Tests: extended `option_set_repo_test.go`, `option_sets_test.go`
  (catalog page handlers), `catalog_directives_test.go`, `cloudsync_test.go`,
  `cloudsync_catalog_wire_test.go`.

## Review findings

1. **BLOCKER (fixed) — docs-shots screenshots not actually regenerated.**
   The first pass hand-edited `web/help/img/manifest.json` (a file whose
   own header says "Do not edit by hand") with a `surface_sha256` that
   didn't match the guard's own computation, and never ran the Playwright
   capture despite the page gaining a rename form, status tag, ↑/↓/×
   buttons, a used-by block and a card footer. **Fix:** ran `make
   docs-shots` for real (120 screenshots, ~2.2 min). The regenerated
   `option-sets.png` in every locale is byte-identical to the prior one —
   confirmed this is legitimate, not a missed regression: the docs-shots
   seed data creates no option set, so the topic's screenshot shows the
   (unchanged) empty/add-form state in every run. The manifest's topic and
   surface hashes are now the tool's real output; `guard-docs-shots.sh`
   passes.
2. **SHOULD-FIX (fixed) — a refusal could render with no visible message
   at all.** `option_sets.html`'s page-wide alert (`ErrorSetID == ""`) sat
   inside the `{{ if not .Sets }}`'s `else` branch, so a 404 on a stale tab
   whose last set had just been deleted elsewhere (zero sets left)
   rendered the empty state with no alert. **Fix:** moved the check above
   the empty/non-empty branch so it always renders when applicable.
   Relatedly, `editOptionSetValues`'s "value id not found" case reused the
   same `ErrOptionSetNotFound` the whole-set-not-found case uses, which
   hardcodes `errSetID=""` — correct for a truly missing set, wrong when
   (as here) the set itself was just fetched successfully and still has a
   card. **Fix:** that branch now renders directly with the real `setID`
   so the message lands on the right card instead of the page-wide slot.
3. **NIT (fixed) — a value's text field was sized by byte length, not
   character count.** `size="{{ len .Value }}"` made an Arabic/Persian
   value's edit field render up to ~2x too wide on an engine that still
   falls back to `size` instead of `field-sizing` (WebKitGTK, a real
   target per this repo's kiosk environments). **Fix:** new template func
   `runelen` (`internal/httpx/httpx.go`) using `utf8.RuneCountInString`;
   also stopped hardcoding `maxlength="128"` in the template in favour of
   passing `MaxValueLen` through the render data, so the template and
   `MaxOptionSetValueRunes` can't drift apart.
4. **NIT (fixed) — a value containing control characters could collide
   with the swap-safe placeholder.** `replaceOptionSetValuesTx` parks each
   kept row on a NUL-prefixed placeholder text while swapping two values'
   text (avoiding a transient UNIQUE collision). A value equal to that
   literal placeholder string would have collided. **Fix:**
   `normalizeOptionSetValues` now rejects any value containing a control
   character (`unicode.IsControl`).
5. **NIT (fixed) — mixed digit scripts in the Persian bound message.**
   `fa.json`'s `value_invalid` mixed a Persian digit (۱) with the Latin
   digits `%d` substitutes, rendering "بین ۱ تا 128 نویسه". Changed the
   literal to Latin `1` for consistency with the substituted bound.
6. **Cross-repo (handled):** the till's own reporting convention
   (`reference/manage-shop-catalog-api.md`) had no entry for either new
   directive type — added §3.11/§3.12 there, mirroring §3.4/§3.10's
   level of detail, plus both types in the `DirectiveMinTillVersion`
   example map (`"0.30.16"`, matching ut-cloud's actual constant).
   `catalog.option_sets.*` is a brand-new core key set, so per this
   repo's own `CLAUDE.md` the `ut-plugin-language-de`/`-es` catch-up PRs
   land in this same cycle (tracked as this cycle's obligation below, not
   a new card).

## Not a finding, logged for follow-through

- **Language-pack PRs owed.** Core merges first (`main` goes red on
  `lang-pack-drift`, expected); `ut-plugin-language-de` and
  `ut-plugin-language-es` catch-up PRs (20 keys each, already drafted and
  validated against each pack's own `scripts/validate.sh`) land in the
  same cycle.
- Modifier groups' own unconditional-delete gap (a known separate issue,
  #3318) was explicitly left untouched — out of this card's scope.

## Verified beyond the automated tests

- TDD claims re-verified independently (revert → run → restore, inline):
  delete-refused-when-in-use, rename-collision, and the values swap-park
  mechanism — each reverted fix produced the real expected failure, not a
  compile error.
- A real driven run against the actual till binary (not just rendered-HTML
  assertions): create a set, add three values, reorder via the ↑/↓
  buttons, rename, delete-when-unused (succeeds), apply a set to a real
  demo item + generate variants + attempt delete-when-in-use (refused,
  "USED BY" chip + explanation rendered inline, Delete disabled; a direct
  server-side call bypassing the disabled button also refused with 409).
  Screenshots taken and looked at: 1024×600 (kiosk floor), 360px (phone),
  and RTL (fa) at 1024×600 — no overlap, clipping or mirroring issues on
  the pages this card changed.
- A real, pre-existing, repo-wide bug was found and filed separately
  during this driven run (not a regression from this card, confirmed by
  reproducing it on `/items`, `/catalog` and `/settings` too): the sticky
  statusbar overlaps the last interactive control on a tall page at a
  narrow viewport. Filed as ut-docs#3413, left out of this branch.
- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...`
  (0 issues), full `go test ./...` (all packages green) and
  `go test -race -timeout 60m ./internal/plugins/...` (green, ~47 min —
  the one package whose race run doesn't fit the default per-package
  timeout), `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-docs-shots.sh` (now genuinely passing, see
  finding 1), and the rest of the standing CI guards — all run and green,
  independently, by Dev, Tester and Reviewer.
- No real shop/client name in any seed or test data (generic "Task
  Runner"-style names only); no secrets introduced.
- `internal/netreach`'s `TestPanickingProbeStillClearsInFlight` under
  `-race` is a pre-existing flake, confirmed reproducing identically on an
  untouched `main` checkout — not this branch's doing, not re-fixed here.

## Addendum — 2026-10-02 sweep (lane:cloud-54): retired items no longer block a delete

**Finding (reported on the PR after the original review):** `DeleteOptionSetIfUnused`
refused while `item_option_sets` had *any* row for the set, including links
to retired items (`items.is_active = 0`). `/catalog` lists only active
items, so such a link could never be removed on the till and the set became
permanently un-deletable — on my. too, which gates Delete on `items_total`.

**Fix:** `ItemsUsingOptionSet`, `ListOptionSetsWithItems` and the in-use
check now count active items only; deleting the set cascades any retired
item's link away (`item_option_sets ON DELETE CASCADE`). Regression test
`TestOptionSetRepo_DeleteOptionSetIfUnused_RetiredItemsDoNotBlock` (red
before the fix: `ItemsUsingOptionSet` returned the retired Hoodie too;
green after). Help topic (en/ar/fa/tr) says a retired item doesn't count and
must have the set re-applied if restored; manifest topic hashes recomputed
(text only, no screenshot changes).

**Independent review (Fable, different model from the Opus 5.5 author):**
no blockers. Verified the cascade removes only `item_variant_options`
provenance — `item_variants` rows, sales and stock are untouched, the same
outcome as the existing unapply-then-delete path. Reactivation is possible
only from my. (`save_item{active:true}`); such an item comes back with its
variants but without the set — accepted and documented in the method
comment and the manual. Should-fixes (this record, the manual) and the
comment nits (`ErrOptionSetInUse` wording, `AssignedItem.IsActive` constant
here, provenance note) applied. Modifier groups intentionally keep listing
retired items (they delete unconditionally).

**Merge of `main`:** only conflict was `web/help/img/manifest.json`
`surface_sha256`; both sides' screenshots/topic hashes merged cleanly, so
the combined surface hash was recomputed (`Docs-Shots-Unchanged: true`).
`guard-docs-shots`, `guard-help-drift`, `guard-help-topics` pass.

**Verdict:** safe to merge once CI is green.
