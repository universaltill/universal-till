# 2026-09-18 — Sane upper bound on modifier-group min_select/max_select (ut-docs#2376)

## What shipped

`item_modifier_groups`' `min_select`/`max_select` had no upper bound at
either till-side validation point — the DB `CHECK` constraint only
requires `min_select >= 0 AND max_select >= min_select`. Both were flagged
(but deliberately not fixed, as "a product decision on the ceiling") by
the independent reviews of ut-docs#2322:
`docs/code-reviews/2026-09-17-modifier-groups-cloudsync-hook.md` finding 3
and the paired `ut-cloud` record's finding 3. Not money/tax-relevant, not
exploitable (both entry points are staff-only, trusted input) — the actual
defect is a nonsensical "choose between 0 and 999999999" render on the
sale-screen picker (`pos_modifiers_api.go`'s selection-count check).

Picked a fixed cap of **50** (matches the card's own "a few dozen"
suggestion and the existing `maxModifierGroupOptions = 50` bounded-constant
already used one const block above in `ut-cloud/internal/claims/
directives.go` for the same feature's option list) — an ordinary
engineering/UX-ceiling judgment call, not a business/legal decision, so no
Admin Review escalation (see ut-docs#2376's own "low priority... no real
shop has hit this" framing).

- `internal/pages/catalog/handlers.go` — `POST /api/catalog/modifier-group`
  (the local admin creator, both create and update paths share this
  handler): new `maxModifierSelect = 50` const, validated after the
  existing required/max-select normalisation, rejected with the existing
  `catalog.error.invalid_request` locale key (no new user-facing string,
  so no i18n follow-up needed).
- `internal/pages/cloudsync_wire.go` — `cloudUpsertModifierGroup` (the
  till-side hook validating an incoming cloud directive): same cap value,
  same rejection shape as its existing `minSelect < 0 || maxSelect < 0`
  check immediately above it.
- Mirrored in `ut-cloud`'s `internal/claims/directives.go` (separate repo,
  own PR) and its `store_detail.html` per-item creator form's `max="50"`
  attribute.

## Out of scope (explicitly, per the card)

- No DB `CHECK`-constraint change / no new migration — this is app-level
  validation only.
- No configurable/admin-settable ceiling — a fixed constant matches the
  card's own low-priority framing.
- No change to `item_modifier_options` bounds (a separate, already-bounded
  field via `maxModifierGroupOptions`).
- No grandfathering of any pre-existing out-of-range row (none exist in
  practice — both current entry points already reject unbounded values
  going forward, and this repo has no evidence any shop has ever hit this).

## Independent review

Sonnet, fresh context, diff read cold, no access to the Dev/BA/Architect
reasoning above.

**Verified against the real code, not the card's assumptions.** Confirmed
all three validation points named in the card (this file's two, plus
`ut-cloud/internal/claims/directives.go`) independently, and confirmed the
"local admin creator" the card originally pointed at
`internal/pages/catalog/handlers.go` (not a different file) really is the
same handler used for both create and update (`groupID == ""` branch vs.
`else`) — both paths share the single validation block added, so no
update-path gap.

**TDD re-verification (mutation testing).** For each of the two changed
files, removed only the new cap check (keeping the new tests), confirmed
`TestCatalogModifiersPanel_RejectsSelectAboveCap` and
`TestCloudUpsertModifierGroup_InvalidMinMaxFails`'s two new subtests
(`max_above_cap`/`min_above_cap`) fail with the fix removed, and the
boundary-accept tests (`TestCatalogModifiersPanel_AcceptsSelectAtCap`,
`TestCloudUpsertModifierGroup_AcceptsSelectAtCap`) still pass either way
(50 was never rejected, fix or no fix) — proving the reject tests actually
exercise the new check and the accept tests prove the cap is inclusive,
not off-by-one. Restored both files and re-built clean afterward.

**Boundary correctness.** Both reject tests use `cap+1` (51), both accept
tests use exactly `cap` (50) — confirms `>` not `>=` is the right operator
(a group asking for exactly 50 selections is legitimate, not an edge case
to refuse).

**No behaviour change to existing normalisation order.** The new check in
`cloudUpsertModifierGroup` sits right after the existing `< 0` check and
before the `maxSelect < 1`/`required` normalisation — an already-invalid
value is refused before any clamping happens, matching the existing
control flow shape rather than inventing a new one.

**CLAUDE.md compliance: clean.** No SQL query text added outside
`internal/data`/`internal/db` (`guard-data-access.sh` passes). No new
locale string (reused `catalog.error.invalid_request`, present in every
`web/locales/*.json`, individually confirmed). No migration added or
edited (append-only rule not implicated — this is app-level validation,
not a DB constraint change, consistent with the card's own explicit
non-goal).

**No findings.** The diff is a narrow, well-scoped mirror of the pattern
the card asked for, in both places the card named, with tests proving both
the reject and the accept side of the boundary.

## Verification

- [x] `go build ./...`, `go vet ./...` — clean
- [x] `gofmt -l .` — clean (touched files individually confirmed)
- [x] `go test ./internal/pages/...` (includes `./internal/pages/catalog/...`) and `./internal/data/...`, `-count=1` — green (full suite, not just the new tests)
- [x] `golangci-lint run ./internal/pages/... ./internal/pages/catalog/...` — 0 issues
- [x] `bash scripts/ci/guard-data-access.sh` — pass
- [x] Mutation-verified both fixes (see "Independent review" above)
- [x] Confirmed `catalog.error.invalid_request` present in every `web/locales/*.json` — no i18n follow-up needed

## Companion change

`ut-cloud` PR (same branch name, `fix/2376-modifier-group-select-cap`)
mirrors this in `internal/claims/directives.go`'s `queueDirective`
validation and adds the matching `max="50"` HTML attribute to
`store_detail.html`'s per-item modifier-group creator form.
