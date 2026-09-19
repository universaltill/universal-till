# 2026-09-19 — Structured void/comp/waste with shrinkage reporting (ut-docs#1465, G41)

## What shipped

A manager-gated, pre-tender void/comp/waste flow with its own shrinkage
report, distinct from a refund of a completed sale (G27):

- `POST /api/pos/remove` (`internal/pages/pos_api.go`): removing a basket
  line whose **extended value** (qty × unit price) is non-zero now requires
  a `reason` (`void`|`comp`|`waste`, `internal/pos/shrinkage_reason.go`)
  and routes through the existing `checkOrElevate(d, r, "void_comp_waste",
  pin)` pattern (`elevation.go`, unmodified) — a role without the new
  `void_comp_waste` permission (cashier, by seeding) gets the standard
  manager-PIN elevation prompt; manager/admin/super_admin proceed with
  just the reason. A zero-value line is removed exactly as before, no
  gate — there's no loss to categorize. Every gated removal writes a
  `shrinkage_events` row and an `audit_log` row (dual-attribution via
  `InsertAudit`/`InsertAuditElevated`, same as every other elevation site).
- New migration `internal/db/migrations/035_shrinkage_events.sql` —
  `shrinkage_events` table (denormalized item name/SKU, no-cascade FK,
  CHECK-constrained `reason_category`) + `void_comp_waste` permission
  seeded to admin/manager/super_admin (not cashier), same
  `INSERT OR IGNORE` idiom as `033_catalog_management_permission.sql`.
- `internal/data/shrinkage_repo.go`: `InsertShrinkageEvent`,
  `ShrinkageByReason`, `ShrinkageTopItems`, mirroring
  `InsertAudit`/`RefundsByWindow`/`TopItems`'s own signature styles.
- New "Shrinkage & Loss" tab under the existing `/reports` page
  (`case "shrinkage"` in `reports_page.go`'s tab switch — no new page
  route, so no new help-topic `routes:` claim needed), visible and
  reachable only to a `void_comp_waste`-holding role, same gating shape
  as the existing `eod` tab.
- UI: a 3-button (Void/Comp/Waste) reason `<dialog>` replaces the plain ✕
  for a non-zero-value basket line (`web/ui/partials/basket.html`,
  `web/public/app.js`/`app.css`); a zero-value line keeps the old
  one-tap ✕ unchanged.
- i18n: ~15 new keys added to and genuinely translated (not machine
  output, not baselined) in all four `web/locales/{en,ar,fa,tr}.json`.
- Manual: `web/help/en/{reports,sell}.md` updated in this branch;
  screenshots regenerated via `make docs-shots`. ar/de/fa/tr help-prose
  translation deferred per this topic's existing English-only precedent
  (tracked the same way as prior entries in
  `scripts/ci/i18n-baseline/help-drift-baseline.json`).

## Independent review

Opus, in an isolated worktree (`isolation: "worktree"`, no shared checkout
with the orchestrating session or the Tester pass). Ran the full gate for
real: `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...`
(60 packages), `golangci-lint run ./...` (0 issues), plus
`guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`, `guard-docs-shots.sh`,
`guard-compliance-claims.sh`, `guard-page-http-error.sh`,
`guard-e2e-fixtures-import.sh` — all green.

**One blocker, fixed:** the diff broke the `e2e` CI job. A priced line's
✕ no longer POSTs `/api/pos/remove` directly — it opens the reason
dialog — so three specs that clicked `.btn-x` expecting immediate removal
(`sale-screen-213.spec.ts`, `codeless-item-shortcut-1459.spec.ts`,
`catalog-image-to-till.spec.ts`) would have failed in CI. Fixed by
clicking the toggle then the first reason button (locale-independent by
position). A fourth site in `sale-screen-213.spec.ts` — the ut-docs#239
htmx-settle-window race guard — needed its *probe* changed, not just its
selector: `.btn-x` is now driven by a delegated, load-once document
listener in `app.js`, so it would "pass" regardless of the settle timing
under test and prove nothing. Switched the probe to the qty stepper's `+`
button (`.qty-step-btn`, genuinely htmx-bound per swap), watching
`/api/pos/line` instead of `/api/pos/remove` — same property under test,
a probe that can actually fail. Re-run for real after pulling the fix out
of the isolated worktree (the review agent had no Chromium/`node_modules`
in its worktree to run Playwright itself): `sale-screen-213.spec.ts`,
`codeless-item-shortcut-1459.spec.ts`, `catalog-image-to-till.spec.ts`,
`basket-no-horizontal-scroll-391.spec.ts` (a `.btn-x` geometry-only spec,
unaffected but re-run to confirm), and `pages.spec.ts`/`manual.spec.ts`
(the other specs touching `/reports`) — all pass, 46/46.

**Two smaller real findings, fixed:**

1. **Handler/template gate mismatch.** `basket.html` shows the reason
   sheet on the line's **unit** price being non-zero; the handler gates
   removal on the **extended** value (`MulQty`-rounded qty × unit price).
   These disagree for a line whose unit price is non-zero but whose
   extended value rounds to zero (a weighed line at a near-zero decoded
   weight) — the operator sees the sheet, taps a reason, and the handler
   takes the zero-value path. `HX-Retarget`/`HX-Reswap` were previously
   set only inside the non-zero branch, so that response's full-basket
   HTML would `innerHTML`-swap into `#shrinkage-hint` — which lives
   *inside* `#basket` — producing a nested duplicate basket. Fixed by
   moving both headers unconditionally before the removal, past both
   early-return paths (400s, the elevation prompt) which are unaffected.
2. **JSON tag convention.** `ShrinkageReasonTotal.ReasonCategory` was
   tagged `json:"reasonCategory"` — the only camelCase tag in
   `internal/data`; `CLAUDE.md` mandates snake_case. Renamed to
   `json:"reason_category"`. Latent (template-only consumer today, one
   call site), zero-risk rename.

**A UX finding from the Tester pass, re-verified independently and fixed
a second time:** the reason sheet stays open behind the elevation-PIN
modal when a cashier without `void_comp_waste` picks a reason (neither
dialog is modal — both need the on-screen keyboard reachable). Fixed in
`elevation_prompt.html`'s self-opening script by closing any open
`.shrinkage-sheet` first (a no-op everywhere else, since nothing else
matches that selector). The reviewer independently confirmed this fix is
real. A related accessibility gap the reviewer flagged but couldn't land
(no Chromium in its worktree to confirm a `docs-shots` regen) — the
toggle's `aria-expanded` was left `"true"` when the sheet was closed this
way, unlike the click-outside path in `app.js` which already resets it —
was fixed here alongside it, verified with a fresh `make docs-shots` pass
(surface hash updated, no visible pixel changed since the dialog isn't
open in any captured page state).

**Two Tester-found UX issues, re-verified independently and confirmed
already fixed by the time of review:**

- ar/tr `shrinkage.reason.void` collided with `common.cancel`
  (`إلغاء`/`إلغاء`, `İptal`/`İptal`) — now `إبطال`/`Storno`, both distinct
  from Cancel in every locale. `shrinkage.error.invalid_reason`'s prose
  updated to match in both locales for consistency.
- The reason dialog overflowed off-screen on a 360px viewport: it's a
  non-modal `<dialog>` (`.show()`, not `.showModal()` — same
  OSK-reachability reasoning as every other dialog in this codebase)
  nested inside a scrolling basket row rather than a top-level element
  like `#hold-modal`/`#elevation-modal`, so it rendered at its row's
  in-flow position instead of being centered. Fixed with
  `position: fixed` + `inset-inline: 0` + `margin-inline: auto` +
  `inset-block-start: 50%`/`translateY(-50%)` centering and a
  `max-height`/`overflow-y: auto` safety net — verified with a real
  Chromium render (a minimal static harness loading `app.css` directly,
  not the full app) at 360×740 in both LTR and RTL: the dialog centers
  correctly, `x: 18` (perfectly centered in a 360px viewport with a
  324px-wide dialog), fully inside the viewport vertically, identical
  geometry in RTL. The app.css comment that had claimed this "just
  works" for a non-modal dialog was corrected to explain why it didn't
  and what actually fixes it.

**A separate, real bug found and fixed independently by Tester before
Reviewer ever saw the diff** (documented here since it landed on this
branch): a checkpoint commit made by the orchestrating session briefly
captured a temporarily-neutered gate (`if false && !extended.IsZero()`)
during a shared-working-tree race with Tester's own false-pass
verification (ut-docs#386-class hazard). Caught by Tester, restored, and
independently reconfirmed correct by Reviewer's own TDD re-verification
below.

## Independent TDD re-verification (Reviewer's own, not taken on faith)

1. **Core gate**: reproduced the `if false && !extended.IsZero()`
   neutering. 5 specific failures, no panics —
   `NonZeroLineMissingReason_400`/`InvalidReason_400` got 200 instead of
   400; `CashierNoPINGetsElevationPrompt` and both elevation tests got a
   removed line with `sql: no rows in result set` on the expected
   `shrinkage_events` row. Restored → green.
2. **Repo aggregation**: flipped `ShrinkageByReason`'s window `AND`→`OR`.
   Failed with an out-of-window row leaking into the total
   (`{comp Count:1 Total:150}` vs. expected). Restored → green.
3. **Migration checksum**: tampered `sku TEXT` → `TEXT NOT NULL` in
   035's SQL. `TestShippedMigrationsUnchanged` failed naming migration
   035 with both the expected and actual hash. Restored → green —
   confirms the pinned checksum genuinely covers this file's exact bytes,
   not a stale/copy-pasted value.

## Confirmed clean, no action needed

- **Money**: every amount is `internal/money.Money`; `.Minor()` used only
  at the DB boundary. `quantity float64`/`REAL` matches `001_init.sql`'s
  own existing quantity-column convention exactly (never mixed with
  money). Report DTOs keep `int64`, consistent with
  `MethodTotal`/`TopItem`/`grandTotal` precedent in the same files.
- **Migration schema**: matches `033`'s idiom exactly; CHECK constraint on
  `reason_category`; `IF NOT EXISTS`/`INSERT OR IGNORE` replay-safe;
  denormalized snapshots and no-cascade FKs consistent with
  `sale_lines`/`audit_log`. No schema changes recommended.
- **Access control**: a cashier hitting `GET /ui/reports/tab/shrinkage`
  directly (not just the hidden tab button) gets a 200 with zero
  shrinkage data — verified via a raw HTTP request, mirroring the
  existing `eod` tab's own gating shape exactly.
- **Recurring bug classes**: zero file I/O anywhere in this diff
  (`os.MkdirAll`/`os.WriteFile`/`filepath.Join`/`paths.*` all absent,
  confirmed by grep, not assumed) — both classes this pipeline keeps
  finding are not applicable here.
- **Scope/security**: no real client/shop name anywhere (fixture names
  are generic — Apple, Banana, Sparkling Water, Free Sample); no
  secret-shaped literal in the diff; kiosk/self-order path
  (`self_order_cart.html`, `KioskEngine.RemoveLine`) is untouched and
  uses a different template entirely — no crossover with this change.
- **Help docs**: `reports.md`/`sell.md` updates are substantive
  descriptions of the real new behavior, not token mentions;
  `guard-help-topics.sh`/`guard-help-drift.sh` both genuinely green.

## Verified beyond automated tests

- Full `go test ./...` (60 packages) and `golangci-lint run ./...` (whole
  repo, 0 issues) after every fix above, not just once at the start.
- Real Playwright run (not assumed) of every e2e spec touching the
  changed surface: `sale-screen-213.spec.ts` (8/8, including the
  ut-docs#239 race guard with its corrected probe),
  `codeless-item-shortcut-1459.spec.ts`, `catalog-image-to-till.spec.ts`
  (4/4), `basket-no-horizontal-scroll-391.spec.ts` (16/16, `.btn-x`
  geometry only — confirmed unaffected), `pages.spec.ts`/`manual.spec.ts`
  (22/22, the two other specs exercising `/reports`) — 58/58 passing.
  Confirmed via `grep` that no other spec in `e2e/tests/` clicks `.btn-x`
  on a basket line or posts to `/api/pos/remove` directly.
- A Tester pass (separate from this review) drove the real running app
  end-to-end with seeded data: zero-value removal unchanged, manager
  session sees the reason sheet with no PIN prompt, cashier session sees
  the real elevation modal and completes it with dual-attribution
  correctly recorded, the report tab renders real numbers matching
  exactly what was seeded, RTL (`ar`) layout checked, 1024×600 kiosk
  floor checked. See that pass's own report for the full surface list;
  not re-run here since it doesn't depend on anything this review's own
  fixes touched.
- `make docs-shots` run twice in this review cycle (once after the WIP
  fixes, once after the `aria-expanded` fix) — both green, screenshots
  and manifest committed fresh.

## Safe to merge

Yes. `merge_method: "merge"` (never squash/rebase — ut-docs#250).

## Explicitly deferred (new Backlog candidates, not this card's scope)

- A pre-existing, unused `void` permission action (seeded in
  `001_init.sql`, granted to admin/manager/super_admin) has zero
  `canPerform` call sites anywhere — looks like dead scaffolding from an
  earlier, never-shipped feature, unrelated to this card's own
  `void_comp_waste` action. Worth a follow-up to confirm safe to remove.
- `permissions.action.void_comp_waste`'s tr/ar wording (İptal/الإلغاء)
  doesn't match `shrinkage.reason.void`'s now-distinct wording
  (Storno/إبطال) — a translation-consistency call, not this review's to
  make.
- `LineDiscount` is not factored into the gate or `extended_value_minor`
  (both use gross qty × unit price) — a line discounted to zero still
  demands a reason + manager PIN. Defensible (gross retail value of the
  loss, and documented in the migration) but a product-owner call if a
  different treatment is wanted later.
- A `shrinkage_events`/`audit_log` insert failure is logged, not
  surfaced — the line removal still succeeds and the loss goes
  unrecorded. Consistent with this product's offline-first,
  never-block-the-till principle, but worth a deliberate follow-up if
  silent data loss here ever needs a stronger guarantee.
- Post-tender voids/comps, CSV export of the new tab, configurable
  reason categories, alerting/thresholds, and per-till shrinkage
  breakdown remain explicit non-goals per the original card scope.
- ar/de/fa/tr translations of the new `reports.md`/`sell.md` help prose,
  same recorded-drift convention as this topic's existing baseline
  entries.
