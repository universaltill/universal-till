# Cloud catalog directives: primary-till gate + audit trail (ut-docs#2353)

## What shipped

`internal/pages/cloudsync_wire.go`'s catalog-mutating cloud directive hooks
had neither of the two protections their local-admin-path siblings have:

- **Primary-till gate.** `items`/`item_variants`/`item_barcodes`/
  `variant_barcodes` are admin-synced tables (`internal/data/
  sync_admin_repo.go`'s `adminTables`, one-way primary-wins pull) — a write
  accepted on a replica till silently vanishes on the next admin pull. The
  local admin handlers (`catalog/handlers.go`) all gate on this
  (`requirePrimary`); the cloud directive hooks did not.
- **Audit trail.** No record distinguished "an operator changed this at the
  till" from "the merchant changed this from the cloud portal."

Fixed for every catalog-mutating directive hook that actually exists in
`cloudsync.Hooks{}` today:

- `cloudCreateItem` (`create_item`) — already had a named function; added
  the gate + audit.
- `cloudSetPrice` (`set_price`), `cloudRenameItem` (`rename_item`),
  `cloudAddBarcode` (`add_barcode`), `cloudDeactivateItem`
  (`deactivate_item`) — these were previously unnamed inline closures in
  the `Hooks{}` literal in `buildCloudHooks`; extracted into named
  functions (mirroring `cloudCreateItem`/`cloudAdjustStock`'s existing
  pattern) so they're independently testable, and gated + audited the same
  way.

Two shared helpers: `requirePrimaryDirective(ctx, d) error` (the gate,
returning a plain error surfaced in the directive's result column — hooks
have no `http.ResponseWriter` to answer with an HTTP status) and
`auditCloudDirective(ctx, d, entityType, entityID, action, payload)` (the
audit write, actor `"system"` — same FK-safety pattern `cloudAdjustStock`
already established for ut-docs#1676, with the row's own write error
logged rather than discarded, since for a mutation with no other audit
trail on either path a dropped insert would misattribute a cloud change as
an operator's own).

`cloudUpsertCategory` does not exist yet (only in an unmerged PR,
ut-docs#2323/universal-till#1210) — out of reach for this change, same as
`cloudSetPrice`/`cloudRenameItem` originally looked before the review
found they existed as unnamed closures.

## Independent review

Opus subagent, isolated worktree, fresh context — hadn't seen the Dev
reasoning. Ran the real build/vet/test/lint/guard-data-access gates,
did a live revert→fail→restore TDD verification of all three original
tests, and independently confirmed the `SyncPrimaryURL` gate polarity and
the `InsertAudit` FK behavior with a throwaway probe against the real
migrated schema. Found and this pass fixed:

1. **(blocker-class, fixed)** The scope rationale was wrong: `cloudSetPrice`/
   `cloudRenameItem`/`cloudAddBarcode`/`cloudDeactivateItem` are all live,
   reachable directive types today — just as unnamed closures rather than
   named functions, which is why a text search for the function names the
   issue used came back empty. All four are now gated + audited.
2. **(fixed)** The audit insert's own error was discarded (`_ =`); now
   logged — for `cloudCreateItem` specifically, the audit row is the *only*
   record of the mutation (the local admin item-create path doesn't audit
   either), so a silently-dropped insert would have inverted the change's
   whole purpose.
3. **(fixed)** Audit payload was `nil`; now carries the actual field(s)
   changed (name/price/barcode) so the trail is readable after a later
   rename/deactivate.
4. **(confirmed, no change)** Audit-insert ordering (after `CreateItem`
   succeeds, before the barcode-attach step) is correct as written — a
   barcode-attach failure returns a result string, never an error, and
   never rolls back the item, so the create is an unconditional fact by
   that point.
5. **(fixed)** `cloudCreateItem`'s primary-gate check now runs *after* the
   idempotency short-circuit, not before — an at-least-once directive
   replay against a replica whose catalog already matches (the normal case,
   since it was pulled down from the primary) now reports success instead
   of a spurious replica refusal.
6. **(fixed)** Two test assertions silently swallowed a query error
   (`_ =` on a `FindActiveItemByName`/audit-count lookup) that could mask
   a false pass; both now assert the error explicitly, and the retry
   test's audit count is now scoped to the actual item's `entity_id`.
7. **(deferred, nit)** The gate's error message could name the primary's
   URL (already available from `SyncPrimaryURL`) rather than just saying
   "make this change on the primary till" — left as-is for now, low
   value versus scope.

## Verified beyond automated tests

- Live TDD revert→fail→restore, twice (once by Dev, once independently by
  Review) — all new tests are genuine red→green, not false-passes.
- `SyncPrimaryURL` polarity cross-checked against 30+ existing call sites;
  confirmed not inverted.
- `InsertAudit`'s `actor_id` FK verified against the real migrated schema
  with a throwaway probe: `"cloud"` (a plausible wrong guess) is rejected
  by the FK, `"system"` (what this diff uses) is the real seeded row.
- Full repo gate: `gofmt -l .` clean, `go build ./...`, `go vet ./...`,
  `go test ./...` (whole repo, all green), `golangci-lint run ./...`
  (0 issues), `guard-data-access.sh` and `guard-i18n.sh` both green.

## Safe to merge

Yes. 21 new/modified regression tests across all five directive hooks
(gate refusal + audit-row-written per hook, item and variant entity types
covered for `AddBarcode`/`DeactivateItem`).

## Explicitly deferred

- `cloudUpsertCategory` — doesn't exist in `main` yet; whichever PR adds it
  (ut-docs#2323/universal-till#1210, still open as of this review) should
  apply the same `requirePrimaryDirective`/`auditCloudDirective` pattern
  when it lands, since its local sibling (`categories_page.go`) already
  has both a `requirePrimary` gate and its own audit call.
- The gate error message naming the primary's URL (review finding #7
  above) — low-value polish, not blocking.
