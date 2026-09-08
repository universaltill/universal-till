# Code review: "Remove sample data" keeps an edited demo item forever (ut-docs#1840)

**Date:** 2026-09-08
**Card:** ut-docs#1840 — "Sample data can't be removed once touched — renaming a
demo item disqualifies it, the message blames 'already in use', and there is no
per-item delete" (German pilot merchant blocked by a mis-ticked setup checkbox)
**Branch:** `fix/1840-remove-sample-data`
**Diff:** `internal/data/demo_seed_repo.go`, `internal/data/demo_seed_repo_test.go`,
`internal/data/seeddata/seeddata.go`, `internal/data/seeddata/remove_demo_relaxed.sql`
(new), `internal/pages/settings_page.go`, `internal/pages/demo_seed_opt_in_test.go`,
`web/ui/pages/settings.html`, `web/public/app.css`,
`web/locales/{en,fa,tr,ar}.json`, `web/help/{en,fa,tr,ar,de}/display.md`,
`web/help/img/manifest.json`. Companion i18n PRs: `ut-plugin-language-de`
and `ut-plugin-language-es` (branch `i18n/1840-remove-sample-data-keys` in
each).
**Reviewer:** independent (Opus, different model, own isolated git worktree,
did not write the code)

## What shipped

The pilot merchant loaded the setup wizard's sample-data checkbox by mistake,
renamed one demo item while poking around, then found "Remove sample data"
would not remove it — and the message it gave ("already in use") was false.
Three compounding defects, all fixed:

1. **The pristine-match rule now relaxes when the till has never traded for
   real.** `DemoSeedRepo.RemoveDemoCatalogue` checks a new till-level gate
   (`demoTillHasNoRealHistorySQL`: no non-sample item anywhere has a live or
   archived `sale_lines`/`stock_movements` row, directly or via a variant).
   When true, it runs a new relaxed removal script
   (`seeddata/remove_demo_relaxed.sql` — identical to the existing
   `remove_demo.sql` minus the sku/name/base_price pristine match; every
   trading-history/held-basket safety clause is unchanged in both variants),
   so an edited-but-otherwise-untouched demo item is removed outright. The
   moment the till has real trading history anywhere, it falls back to the
   original strict script, unchanged.
2. **Kept items are now named and reasoned, not a bare count.** The method's
   return type changed from `(removed, kept int, err error)` to
   `(removed int, kept []KeptDemoItem, err error)`; each row carries
   `KeptReasonEdited` / `KeptReasonHistory` / `KeptReasonHeld` (priority
   held > history > edited when more than one applies), computed by a shared
   SQL `CASE` (`demoItemReasonCaseSQL`) that mirrors `remove_demo.sql`'s own
   safety predicate exactly.
3. **A per-item resolution for an edited-and-otherwise-safe item.**
   `RemoveDemoItem` ("remove anyway") and `KeepDemoItemAsOwn` ("keep as my
   own item") on `DemoSeedRepo`, wired to two new elevation-gated, audited
   routes (`POST /api/settings/demo-item/{id}/remove` / `/keep`), rendered
   inline in the kept-items list. `RemoveDemoItem` re-checks server-side
   (never trusts the client's last-rendered reason) and refuses
   (`ErrDemoItemHasHistory`) if the item genuinely has trading/held-basket
   history.
4. **The "genuinely blocked" message now names the real mechanism** —
   deactivate the item from Catalog, then run Catalog cleanup from
   Settings → Data (manager only) — instead of a blanket "already in use".

Manual (`web/help/{en,fa,tr,ar,de}/display.md`, item 9) rewritten to describe
the new behaviour; screenshots regenerated (`make docs-shots` — no actual
pixel content changed for the touched screen, confirmed by `git diff --stat`
showing only `manifest.json`'s tracked hashes, twice, across two regen
passes). i18n: 15 new/changed keys added to all four core locales and to the
external `ut-plugin-language-{de,es}` packs (their own PRs, verified against
this card's local `en.json` via `UT_CORE_EN_JSON=<path>
scripts/check-key-drift.sh` — zero new drift in either pack; the pre-existing
9 orphan keys `check-key-drift.sh` reports in both packs are unrelated,
confirmed present before this change too, from another in-flight lane's
pending voucher work).

## What the independent review found

Verdict: no data-safety defect. The SQL relaxation is exactly and only the
pristine-match removal (comment-stripped diff of the two `.sql` files
confirmed byte-identical otherwise); the TOCTOU window between the safety
check and the delete is closed by `db.go`'s `_txlock=immediate`; the
hand-rolled cascade in `RemoveDemoItem` covers every non-`CASCADE` FK to
`items`/`item_variants` in `001_init.sql`; `RemoveDemoCustomersPromos` is
untouched. But it found a cluster of real should-fix bugs on the *new*
surface, all fixed in this branch before merge:

- **F1 (should-fix):** the "genuinely blocked" message said "use Catalog
  cleanup below", which is wrong (Catalog cleanup renders ABOVE the Sample
  Data section on `/settings`, in a manager-only block) and invisible to a
  non-manager viewing the Sample Data card at all (gated only on
  `sampleCount`, not `isManager`). **Fixed**: reworded to name the actual
  location (Settings → Data → Catalog cleanup, manager only) instead of a
  positional "below", in every locale including the external de/es packs
  and `display.md`.
- **F2 (should-fix):** the two new per-item endpoints set
  `X-UT-Response: ok` on their elevated success path — that header tells
  `elevation_prompt.html`'s retry form to `window.location.reload()`,
  wiping out every OTHER kept row the merchant hadn't resolved yet, after
  *every single item* resolved via the PIN dialog. **Fixed**: removed;
  matches the bulk `remove-demo-catalogue` handler, which never sets it.
  Regression test added
  (`TestSettingsRemoveDemoItemEndpoint_ElevatedSuccessDoesNotSetReloadHeader`).
- **F3 (should-fix):** the `{id}` path param was used to build the
  elevation prompt's `hxTarget`/summary before being validated — an
  unknown id would render a manager-PIN dialog for an action that was
  always going to be a no-op, and the bare `#`+id selector could break on
  a value needing CSS escaping. **Fixed**: added `DemoSeedRepo.IsSampleItem`,
  checked BEFORE `checkOrElevate` (mirrors `dismiss-pending-base-plugin`'s
  own established `matched` check), and switched to the attribute-selector
  form (`demoItemMsgSelector`). Regression test added
  (`TestSettingsDemoItemEndpoints_UnknownIDNeverElevates`).
- **F4 (should-fix):** the entire `KeptReasonEdited` UI branch (both
  buttons' `hx-post` URLs, the shared message-span id they both target —
  precisely the ut-docs#865 F1 invariant the code comment claims to
  preserve) had no test at all, nor did `KeptReasonHeld` as a *reported*
  reason (only as a count), nor did `RemoveDemoItem`'s refusal via the
  `held_sales_archive` arm specifically. **Fixed**: added
  `TestSettingsRemoveDemoCatalogueEndpoint_EditedItemRowHasBothButtons`,
  a `KeptReasonHeld` assertion in
  `TestRemoveDemoCatalogueKeepsHeldSaleItem`, and
  `TestRemoveDemoItemRefusesHeldArchiveItem`.
- **F5 (nit):** `settings.data.demo_confirm` still promised the old
  per-item-history behaviour ("already in use are kept") after the
  relaxed-mode change means an edited item is now deleted outright when
  the till has no real trading history. **Fixed**: reworded in all locales.
- **F6 (nit):** a comment referenced a `demoItemKeptReason` function that
  doesn't exist (the check is inlined in `RemoveDemoItem`). **Fixed.**
- **F7 (nit):** `keptDemoItems` doesn't join `demo_seed_items` the way the
  removal scripts do, so a hypothetical `is_sample_data=1` row outside the
  seeded id set would fall through to `ReasonEdited`. Harmless today
  (`demo_catalogue.sql` is the only writer and seeds exactly
  `seeddata.ItemIDs`) but undocumented. **Fixed**: documented the
  invariant at the call site.
- **F8 (nit):** no CSS for the new list — already fixed in an earlier
  commit on this branch (`.demo-kept-list`/`.demo-kept-item` in
  `web/public/app.css`, logical properties throughout for RTL) before the
  review report arrived; the reviewer's snapshot predated that commit.
- **F9 (nit):** after one button in a row resolved the item, the OTHER
  button stayed live and returned a confusing "already gone" on a stale
  click. **Fixed**: a small inline script (same `allowScriptTags`
  convention this codebase already uses for post-swap behaviour, e.g.
  `elevation_prompt.html`'s own `dialog.show()`) disables both buttons in
  the row once either one succeeds.
- **F10 (out of scope, filed separately):** `RemoveDemoCustomersPromos`
  has the identical pristine-disqualifies-forever shape for demo
  customers/promo codes. Explicitly out of scope for this card (which
  reports the catalogue-item bug specifically); filed as its own card,
  ut-docs#1858.

## TDD re-verification

Done twice by the independent reviewer, in its own isolated worktree (never
the orchestrator's shared checkout):

1. **Compile-level revert**: `git checkout main -- internal/data/demo_seed_repo.go
   internal/data/seeddata/seeddata.go && rm
   internal/data/seeddata/remove_demo_relaxed.sql` — the test suite failed
   to compile (`kept` is `int` on `main`, `len(kept)` on the fix), a weak
   signal on its own.
2. **Behavioural isolation**: restored the fix, then forced strict mode
   (`if relaxed && false`) to mimic pre-fix behaviour and re-ran the new
   test — `TestRemoveDemoCatalogueRemovesEditedItemsWhenTillHasNoRealHistory`
   failed with `removed 47, kept 3; want 50, 0`, exactly the old buggy
   counts the card describes, while the strict-mode counterpart test still
   passed (proving it isn't vacuously dependent on the relaxation).
3. **Restore**: `git checkout HEAD -- internal/data/demo_seed_repo.go`,
   confirmed clean `git status`/empty `git diff --stat`, full suite green
   again.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...`
  (0 issues) — all clean, run twice (before and after the review-fix
  round).
- `go test ./...` — full suite green, both rounds.
- All CI-blocking guards in `ci.yml`'s `build` job run directly:
  `guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`,
  `guard-page-http-error`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-help-topics`, `guard-webkit-version`,
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`,
  `guard-autofill-suppression`, `guard-e2e-fixtures-import`,
  `check-brand-assets`, `guard-makefile-version` — all pass.
- Repository pattern: read the diff myself in addition to the guard — no
  Go-string-built SQL outside `internal/data`.
- No real client/shop name used as demo/seed/test data; no secret-shaped
  literal anywhere in the diff.
- Language packs: `scripts/validate.sh` and (with
  `UT_CORE_EN_JSON=<local en.json>`) `scripts/check-key-drift.sh` both pass
  clean in `ut-plugin-language-de` and `ut-plugin-language-es`.

## Explicitly deferred

- ut-docs#1858 (F10): the customers/promos side of "Remove sample data"
  has the same pristine-disqualifies-forever shape. Filed, not fixed here.
- fa/tr/ar/de/es wording quality: verified key parity, format-verb
  preservation (`%d`/`%s`), and that the F1 wording fix was propagated to
  every locale, but native-fluency review of naturalness/register in
  those languages was not performed by either the author or the reviewer.

## Verdict

**Safe to merge.** No data-safety defect at any point in the review; every
should-fix finding from the independent pass is fixed and covered by a new
regression test; full gate green.
