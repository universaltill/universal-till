# Code review: catalog item form "Active" checkbox silently no-ops (ut-docs#1367)

**Date:** 2026-08-31
**Card:** universaltill/ut-docs#1367 (`complexity:easy`, found during independent review of ut-docs#1363)
**Dev:** inline (Sonnet, session model — easy card, per model routing)
**Review:** fresh-context Sonnet subagent, isolated worktree

## What shipped

The catalog item-edit form's "Active" checkbox (`web/ui/pages/catalog.html`)
had no paired `<input type="hidden" name="isActive" value="0">`, unlike the
variant/modifier-group forms in `catalog_variants.html`, which already use
this convention. An unchecked HTML checkbox submits nothing for its field
name, so unchecking Active and saving returned 200 but silently left the
item active — no error, no visible sign the deactivation didn't take.

- `web/ui/pages/catalog.html`: added the missing hidden `isActive=0`
  fallback, matching the existing convention.
- `internal/pages/catalog/handlers.go`: this surfaced a second, more
  serious latent bug — `parseItemInput` read `IsActive` via the naive
  `r.Form.Get("isActive") != "0"`. Once the hidden input exists, a
  *checked* box submits both the hidden `"0"` and the checkbox's `"1"`, in
  that DOM order; `url.Values.Get` returns the first value, so every
  normal save of an active item would have started reading it as inactive.
  Switched to the existing `formCheckboxActive(r)` helper — already used
  by the variant/modifier-group forms for exactly this reason — which
  scans every submitted value instead of trusting the first.
- New Go regression test: `TestCatalogItemUpdate_UncheckActive_ActuallyDeactivates`
  (`handlers_test.go`) — create (checked, dual value) stays active,
  uncheck+save (hidden-only) deactivates, re-check+save (dual value again)
  reactivates.
- New e2e spec: `e2e/tests/catalog-active-checkbox-1367.spec.ts` — drives a
  real browser checkbox uncheck + save, asserts the row disappears
  (inactive items have no row, per the existing OOB-delete behaviour from
  ut-docs#1363, already on `main`).
- `web/help/img/manifest.json`: surface-hash bump only (`make docs-shots`
  re-run because `handlers.go`/`catalog.html` are surface-hashed files) —
  zero pixels actually changed, confirmed by discarding the regenerated
  PNGs (byte-identical to render noise) and keeping only the manifest.

## Independent review

Fresh-context `Agent` (no model override — easy card, "different model"
relaxes to "different instance" per model routing) in an isolated `git
worktree` off a `WIP: pre-review snapshot` commit.

**Verdict: safe to merge**, no blocking issues. The review actually ran
the fix (not just read it): full gate (gofmt/build/vet/targeted+full `go
test`/`guard-data-access.sh`/`guard-i18n.sh`/`guard-docs-shots.sh`/
`guard-help-topics.sh`/e2e — 10/10), and did 2 separate TDD
revert-then-restore checks — one isolating the Go fix, one isolating the
HTML fix — each reproducing the exact claimed failure mode before being
restored.

Findings, all resolved as non-issues on inspection (no code changes
needed):

- **Is `formCheckboxActive` overkill vs. just reordering the hidden input
  after the checkbox?** Reviewed and judged correct as shipped: the
  variant/modifier-group rows this convention originated from use
  `form="vf-{{.ID}}"`-associated fields living outside their owning
  `<form>` tag, where HTML's form-submission order follows document tree
  order rather than source order inside one tag — "put hidden after
  checkbox" isn't reliably controllable there. Scanning every submitted
  value is order-independent; reusing the same helper for the simpler
  item form is consistency with the convention that has to be
  order-independent elsewhere, not unnecessary complexity.
- **Every other caller of `parseItemInput`** (2 call sites: create,
  update) — enumerated every existing test posting to either endpoint;
  for the three shapes any of them send (`isActive=1` alone, `isActive=0`
  alone, field omitted), `formCheckboxActive` and the old `Form.Get(...)
  != "0"` check are byte-identical in outcome. No behaviour change for
  any existing caller, confirmed by the unchanged full test suite.
- **The row-click JS unconditionally sets `#item-active.checked = true`**
  when loading an item into the form, not reading the row's actual active
  state — reviewed and confirmed harmless: `ListItems` only ever queries
  active items, so a row can only exist (and therefore only be clickable)
  while its item is active. No reachable dead-state case.
- **i18n**: zero new user-facing strings — confirmed by `guard-i18n.sh`
  and by reading the diff (hidden input, one HTML comment, Go logic only).
- **Manual**: `web/help/en/catalog.md` has no prose describing the Active
  checkbox at all, so nothing had gone stale — this restores already-
  implied behaviour rather than documenting something new.
- No real client/shop name as demo data (test data: "Croissant", "Active
  Checkbox Probe …"); no literal secrets.

One process note from the reviewer, not a code finding: its isolated
worktree initially landed on `main`'s tip rather than the branch under
review (a branch-name lock collision with the shared checkout already
having that branch checked out out) — caught by the reviewer itself
before drawing any conclusions (the `guard-docs-shots.sh` hash didn't
match), and worked around by checking out the WIP commit SHA directly.
Everything above is from the corrected, verified state. Worth a look at
the workflow-authoring worktree-provisioning step if this recurs.

## Verification (beyond automated tests)

- Full `go test ./...` — all packages pass (orchestrator + reviewer, each
  run independently).
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-e2e-fixtures-import.sh`,
  `guard-compliance-claims.sh` — all clean.
- e2e: the new spec plus the adjacent catalog specs most likely to
  regress (`catalog-row-oob-1363.spec.ts`, `catalog-save-notice-917.spec.ts`,
  `catalog-thumbnail-no-request.spec.ts`, `catalog-import-friendly-errors.spec.ts`,
  `catalog-image-to-till.spec.ts`) — all green, re-run after every fix
  iteration. Full default-project e2e suite also run clean (0 failures).
- TDD re-verification, done independently by both Dev and Reviewer:
  reverting either half of the fix in isolation reproduces the exact
  claimed failure mode (a real assertion error, not a panic/compile
  error); restoring returns both to green.
- No real client/shop name used as demo data; no literal secrets
  introduced.

## Safe-to-merge verdict

**Yes.** No blocking findings. Every non-blocking question the review
raised was investigated and resolved as correct-as-shipped — nothing
deferred, nothing filed as a follow-up card.
