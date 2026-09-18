# Review: bulk-delete unassigned modifier groups (universaltill/ut-docs#2406)

**Card:** `/modifiers` shows a count of unassigned modifier groups (no
category link, no item link) and offers "Delete all unassigned groups" in
one confirmed action, plus an optional "Show only unassigned" filter.
Follow-up from the independent review of ut-docs#2399 (finding L5) — since
ADR-0101 a modifier group is shop-wide, and deleting an item never deletes
its group, only its link; a shop that cleans up many single-use-group
items ends up with a pile of orphaned cards and only a per-card delete.

**Complexity:** medium. Dev at Sonnet (inline), review at Opus (fresh
subagent, isolated worktree).

## What shipped

- `internal/data.ModifierRepo.DeleteUnassignedGroups(ctx) (int, error)` —
  one `DELETE ... WHERE id NOT IN (...) AND id NOT IN (...)` against the
  two link tables, cascading options/opt-outs via the same FK
  (`ON DELETE CASCADE`) `DeleteGroup` already relies on. Returns the count
  deleted; zero is a valid non-error result.
- `internal/pages/catalog/handlers.go`: `modifiersPageData` now also
  returns `UnassignedCount` (same "neither Categories nor Items" test the
  page's own `.modifier-unassigned` hint already uses — one definition,
  not two). New route `POST /api/catalog/modifier-group/delete-unassigned`,
  gated and dispatched identically to the existing single-group delete
  (`requireCatalogManagement`, `requirePrimary`, `renderModifierMutationResult`).
- `web/ui/pages/modifiers.html`: a toolbar (count + "Show only unassigned"
  checkbox + "Delete all unassigned groups" button) living *inside*
  `#modifiers-list` so it disappears/updates on the same outerHTML swap
  that changes the group list — it would go stale if it sat in the
  page-level `content` block instead. Client-side-only filter (every card
  is already in the DOM); confirm text server-renders the exact count.
- 4 new i18n keys × 4 core locales (en/ar/fa/tr); `web/help/en/catalog.md`
  updated (new toolbar documented, and a now-stale claim from before
  ADR-0101 — "a group only ever attached to removed items goes with
  them" — corrected to the current, correct behaviour); `make docs-shots`
  re-run (124/124, byte-identical PNGs after the CSS fix below, only the
  surface hash moved).
- Tests: repo-level (`TestModifierRepo_DeleteUnassignedGroups` +
  inactive-link invariant, see below), handler-level (count rendering,
  toolbar hidden at zero, bulk delete leaves assigned groups + cascades
  options, replica-refusal, added to the existing cashier/manager gate
  route list), and a real e2e spec
  (`modifiers-bulk-delete-unassigned-2406.spec.ts`) driving the actual
  page: creates 2 unassigned + 1 assigned group, exercises the filter
  toggle, clicks through the confirm dialog, verifies the assigned group
  survives and the toolbar disappears once nothing is left unassigned.

## Independent review (Opus, isolated worktree)

**Verdict: SAFE TO MERGE.**

Findings, all resolved:

1. **Minor (UI), fixed.** The new toolbar classes
   (`.modifiers-unassigned-toolbar` / `.modifiers-filter-unassigned` /
   `.modifier-card-unassigned`) had no CSS rules at all — at the
   1024×600 kiosk floor it rendered full-width above the existing 44rem
   card stack (the exact "full-width form above a narrower stack" shape
   ut-docs#2399's own Tester note fixed for the create card) and its
   three children collapsed onto one line with no spacing. Fixed in
   `web/public/app.css` (flex + gap, same `max-inline-size: 44rem`
   column, `margin-inline-start: auto` on the button, `body.kiosk`'s
   2.75rem touch target on the filter label) — logical properties only,
   RTL needs no extra rule. Re-verified visually (screenshots at
   1024×600, checked into this session, not the repo) before and after.
2. **Minor (test coverage), fixed.** Nothing pinned that a group linked
   only to an *inactive* item/category must survive the bulk delete —
   the delete reads raw link-table rows with no `is_active` filter, so
   it's correct today, but nothing stopped a future "optimisation" from
   adding one and silently wiping every deactivated item's groups. Added
   `TestModifierRepo_DeleteUnassignedGroups_InactiveLinksStillCountAsAssigned`.
3. **Minor (race), accepted, not fixed.** The confirm dialog names a
   count rendered by the previous GET; if a group is created or
   unassigned in another tab between render and click, the actual delete
   count can exceed what was confirmed. Bounded impact (manager/admin +
   primary-till gated; can never remove a group still offered at
   checkout) and no worse than any other stale-read UI on this page.
   Logged as a possible follow-up (server-side expected-count check,
   mirroring ut-docs#2046's stale-picker 409) rather than blocking this
   card on it — filed as `ut-docs#2421`.
4. **Nit, fixed.** `SELECT DISTINCT group_id` inside `NOT IN (...)` was
   redundant (`IN` already dedups) — simplified to a plain `SELECT`.
5. **Nit, accepted as-is.** The client-side filter resets on every
   fragment swap (e.g. deleting one card individually while filtered
   clears the filter) — already called out in the template's own
   comment; not worth persisting across a mutation that changes the very
   set being filtered.

Verified independently, not taken on trust:

- SQL safety: both link tables declare `group_id TEXT NOT NULL`, so the
  classic `NOT IN` + NULL trap can't silently make the delete a no-op
  forever. `foreign_keys` is genuinely `ON` (`internal/db/db.go`), same
  mechanism the existing single-group `DeleteGroup` relies on for its own
  cascade.
- The "unassigned" definition can only disagree between the UI read and
  the DB delete in the *safe* direction (a dangling link row would keep a
  group the UI already shows as unassigned; the UI can never show
  "assigned" for a group with no link row).
- Gating is byte-for-byte identical to the sibling single-group route:
  method check → `requireCatalogManagement` → `requirePrimary` →
  `renderModifierMutationResult` (which sets `HX-Trigger: modifiers-changed`
  only on 200 — confirmed by the handler test).
- Past-sales claim confirmed structurally: `sale_line_modifiers` and its
  archive carry a FK only on `sale_line_id`; `group_id`/`option_id` are
  plain nullable columns beside their own name/price snapshots.
- The two recurring bug classes this pipeline watches for do not apply
  here: no file writes (no missing `os.MkdirAll`), no filesystem paths at
  all (no cwd-relative path where `paths.Data(...)` belongs) — checked
  explicitly, not skipped.
- TDD re-verified personally: renaming the new repo method, and
  separately gutting the SQL's WHERE clause down to `1 = 0` or dropping
  the category-link half, made the exact tests that should catch each
  failure mode fail with the expected wrong counts; restoring passed
  again.

## Verified beyond automated tests

- Full gate: `go build ./...`, `go vet ./...`, `gofmt -l` (clean),
  `golangci-lint run` (0 issues) on the touched packages, full
  `go test ./...` (repo-wide, green).
- CI guards: `guard-i18n.sh`, `guard-data-access.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` (only pre-existing
  baselined drift elsewhere, nothing new), `guard-docs-shots.sh` (fresh
  after `make docs-shots`), `guard-e2e-fixtures-import.sh`.
- Real driven run: the new e2e spec passed against a real headless
  Chromium instance (pre-installed browser, no network fetch), and was
  re-run again after the CSS fix. Manual screenshots at 1024×600 in
  English and at `?lang=fa` (RTL) confirmed layout, spacing and mirrored
  alignment before write-up.

## Deferred / follow-up

- **ut-docs#2421** (new Backlog card): consider a server-side
  expected-count check on the bulk-delete confirm, mirroring ut-docs#2046's
  stale-attach-picker 409, to close the small race window finding 3
  describes. Not blocking this card.
- `web/help/{ar,de,fa,tr}/catalog.md` were not updated for this specific
  addition — this file already carries pre-existing, tracked
  English-only drift (ut-docs#329/#341) that this change's structural
  counters (`guard-help-drift.sh`) confirm did not worsen.

**Safe to merge.**
