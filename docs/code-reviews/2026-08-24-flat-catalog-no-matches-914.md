# 2026-08-24 — sale screen: flat-catalog search gets "no matches" feedback (ut-docs#914)

## What shipped

Follow-up from the `ut-docs#422` review. `web/ui/partials/buttons.html`'s
`$flat` render branch — a till where no category has ever been assigned to
anything, so `BuildCategoryGroups`'s only group is the synthetic
uncategorized bucket — renders tiles directly in a bare `.grid` with no
wrapping `category-group` section. `#422` added a `sectionHasMatch`-gated
"no matches" message to the tabbed branch and the single-real-category
(no-tab-bar) branch, but explicitly left `$flat` untouched: a search
matching nothing in a flat catalog left a blank grid with zero feedback.
Pre-existing gap, not a regression from `#422` (the `$flat` branch was
never touched by that change).

### Fix

- `web/ui/partials/buttons.html`: added
  `<p class="empty" x-show="!sectionHasMatch($el.parentElement)">{{ T
  "products.no_matches" }}</p>` inside the `$flat` branch, wired the same
  way the no-tab-bar branch's message already is (`$el.parentElement`
  resolves to `#buttons-grid`, the only content rendered in this branch —
  no cross-contamination risk since the three branches are mutually
  exclusive `if`/`else if`/`else`).
- New regression test `TestButtonsHTTPList_FlatCatalogAlsoCarriesNoMatchesMessage`
  in `internal/ui/buttons_search_visibility_test.go`, following the
  existing pattern in the same file (a zero-category fixture, confirms no
  tab bar / no category-group wrapper, confirms the `sectionHasMatch`
  wiring and `products.no_matches` key are present).
- Reuses the existing `products.no_matches` locale key (already translated
  in all four locales) — no new i18n key needed.
- `web/help/img/manifest.json`'s `surface_sha256` bumped since
  `buttons.html` is inside the guarded UI surface — no screenshot pixels
  actually changed (the new line is behind an `x-show` only visible on a
  zero-result search, which the manual's screenshots don't capture), and
  `guard-docs-shots.sh` recomputing and matching the committed hash live
  confirms this is a real, not hand-edited, freshness record.

## Independent review

One round, fresh-context **Sonnet** subagent (`complexity:easy`, per the
scrum-master skill's model routing — a clean-context instance that never
saw the implementation reasoning, run in an isolated worktree).

**Verdict: safe to merge, no blocking issues.**

The reviewer's specific focus was whether `$el.parentElement` in the
`$flat` branch actually resolves to the right DOM scope, given `$flat`'s
grid isn't wrapped in a `category-group` the way the no-tab-bar branch's
is. Confirmed structurally correct: both branches render their `<p
class="empty">` as a direct sibling of the tile grid under `#buttons-grid`,
so `$el.parentElement` resolves to the same container in both cases —
consistent with its closest analog, not a copy-paste mistake. The tabbed
branch scopes one level deeper (`.products-tab-panel`) because multiple
tab panels coexist simultaneously in that branch's DOM; `$flat` doesn't
need that since it's the only content rendered.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- `go test ./internal/ui/...` — all ~40 tests pass, including the new one.
- `bash scripts/ci/guard-i18n.sh` and `bash scripts/ci/guard-docs-shots.sh`
  both green — the docs-shots guard recomputing the surface hash live and
  matching the committed manifest value confirms the hash bump is real.
- `gofmt -l` clean on the changed test file.
- TDD claim independently re-verified by the reviewer: reverted
  `buttons.html` to its pre-fix state, re-ran the new test — real
  assertion failure (not a compile error), dumped body confirmed the
  missing `<p class="empty">`/`sectionHasMatch` output. Restored the fix,
  re-ran — passes again, working tree bit-identical to `HEAD`.
- `products.no_matches` confirmed present with real translations in all
  four locale files (en/ar/fa/tr) via direct grep, not just guard output.
- UX-guidelines checklist: no new CSS classes/tokens (copied verbatim from
  sibling branches), no `left`/`right` hardcoding (RTL-safe), no new
  string (reuses an already-shipped translation, no long-string risk).
- Manual (`web/help/en/sell.md`) already describes search/no-matches
  behavior generically without calling out this specific edge case —
  left as-is since this fix brings `$flat` in line with already-documented
  behavior rather than introducing new user-facing behavior.

## Non-blocking note (not filed as a follow-up — pre-existing, shared by all three branches)

The `.grid`/`.products-tab-panel` container itself doesn't collapse when
every tile is filtered out (only individual `.btn-tile`s hide), so there's
a small amount of empty grid padding above the "no matches" message in all
three render branches. Pre-existing, not introduced by this diff.

## Safe-to-merge verdict

**Yes.** Minimal, well-scoped diff; independent review found nothing
blocking; full gate green; TDD claim independently re-verified via
revert/restore. Merging with `merge_method: "merge"` (never squash/rebase
— ut-docs#250) once CI is confirmed green on the PR's head.
