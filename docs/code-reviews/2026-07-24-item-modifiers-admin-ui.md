# 2026-07-24 — Admin catalog UI for item modifiers, closing task 2 (ADR-0020)

## Context
The final piece of item modifiers (Phase 1 of spec 011): managers can now
create/edit/deactivate modifier groups and their options directly from the
item-detail panel on the catalog page. Same soft-deactivate convention as
items/variants throughout (no hard delete), so a group/option disappearing
from the sale-time picker never orphans a historical `sale_line_modifiers`
snapshot row.

## Design
- New `ModifierRepo.ListAllGroupsForItem` (active AND inactive) backs the
  admin view, distinct from the sale-time `ListGroupsForItem` — a manager
  needs to see and reactivate something a cashier must never be offered.
- A required group is forced to `min_select >= 1` server-side on both
  create and update.
- Two new handlers, `POST /api/catalog/modifier-group` and
  `POST /api/catalog/modifier-option`, following the exact create-vs-
  update-by-ID-presence pattern already used by `/api/catalog/variant`.

## Independent review
Opus-model review, adversarial brief, weighted toward whether a manager
can actually deactivate something through the UI (not just whether the
happy path renders) and money/currency correctness.

**Confirmed correct:**
- No SQL injection surface in the new `ListAllGroupsForItem`/
  `listGroupsForItem` query-string branching — only a fixed literal is
  ever conditionally appended, `itemID` stays parameterized throughout.
- `required` forcing `min_select >= 1` applies on both create and update;
  the sale-time picker (already shipped) validates purely against
  `min_select`/`max_select`, so `required` itself is a display/authoring
  aid, not a second enforcement path to keep in sync.
- Authorization is consistent with every neighboring catalog-admin
  endpoint (`/api/catalog/variant`, `/api/catalog/item/update`, etc.) —
  none of them currently check a manager role. A pre-existing gap across
  the whole catalog-admin surface, not something this change introduces
  or needs to fix in isolation.
- The shared hand-rolled test schema (`internal/testsupport/sqlite_catalog.go`)
  now includes the two new tables with column types matching the real
  migration; no other existing catalog test broke from the addition.

**Fixed (two real, independently-confirmed findings):**
- **HIGH — a manager could not actually deactivate a modifier group or
  option through the UI.** The "Active" checkbox had no paired hidden
  `isActive=0` fallback input, unlike the existing variant form's
  documented "an unchecked box submits nothing, so the panel pairs it
  with a hidden isActive=0" convention. An unchecked box therefore
  submitted no `isActive` field at all; the handler's
  `r.Form.Get("isActive") != "0"` then read the empty string as `!= "0"`
  → always `true`. Caught independently before I'd finished verifying it
  myself (I found and started fixing the same bug while the review was
  running). Fixed properly, not just patched: added the hidden fallback
  input to both the group and option row templates, and replaced the
  naive `Get`-based check in both handlers with a new
  `formCheckboxActive` helper that scans every submitted value for a
  `"1"` — required because a CHECKED box submits both the hidden `"0"`
  and the checkbox's `"1"`, in DOM order (hidden first), so a plain
  `Form.Get` would always see `"0"` first and misread even a checked box
  as inactive. The original test (`TestCatalogModifiersPanel_CreateAndUpdateOption`'s
  deactivate half) hand-wrote `isActive=0` in its POST body — a value a
  real unchecked checkbox never explicitly sends — so it passed against
  both the broken and fixed code and couldn't have caught this. Replaced
  with a reactivate/deactivate pair that submits exactly the multi-value
  shape a real browser produces, plus a standalone unit test
  (`TestFormCheckboxActive_MatchesRealBrowserSubmission`) covering every
  shape directly against the parsing helper. Re-verified live against a
  real built binary submitting the exact wire format a browser sends.
- **MEDIUM — hardcoded `*100` major→minor price conversion breaks every
  0-decimal currency this app supports** (IRR, IRT, IQD, AFN, JPY — see
  `httpx.currencies`). A shop on one of those currencies entering "5000"
  for a modifier's price delta would have had it stored as 500000 minor
  units — a 100x inflation. Consistent with a pre-existing sibling
  endpoint (`/api/catalog/item-cost`, noted but explicitly out of scope
  to fix here) but a regression relative to the currency-correct variant
  price path. Fixed to look up the shop's actual `currency.Decimals`
  (`httpx.CurrencyByCode`) and scale by `10^decimals` instead of a fixed
  100; regression tested against IRR.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` (full suite, zero
regressions), `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-i18n.sh` — all green, both before and after the
review-driven fixes, and independently by the reviewer. Live-verified
against a real built binary twice: once end-to-end (admin creates a
group/option, immediately visible in the live cashier picker on the same
DB, deactivate hides it from the picker while keeping it visible in
admin) and once specifically re-proving the checkbox fix by submitting
the exact multi-value wire format ("isActive=0&isActive=1" for checked,
"isActive=0" alone for unchecked) a real browser produces. New/updated
tests: `TestCatalogModifiersPanel_CreateGroup`,
`TestCatalogModifiersPanel_RequiredGroupForcesMinSelectAtLeastOne`,
`TestCatalogModifiersPanel_CreateAndUpdateOption` (rewritten to submit
browser-realistic form bodies for both deactivate and reactivate),
`TestCatalogModifiersPanel_ShowsDeactivatedGroupsForReactivation`,
`TestFormCheckboxActive_MatchesRealBrowserSubmission`,
`TestCatalogModifiersPanel_OptionPrice_RespectsZeroDecimalCurrency`.
