# Code review: `upsert_modifier_group` cloudsync hook — till side

**Date:** 2026-09-17
**Card:** ut-docs#2322 ("ut-cloud shop panel: modifier groups editor"), a
slice of ut-docs#2289 (Phase A of the cloud shop-panel remote-management
epic). Design: ADR-0095 ("Portal→till configuration — directive-based
writes, till stays authoritative").
**Complexity:** medium (Dev via Sonnet, review via Opus, per model routing).
**Paired record:** `ut-cloud/docs/code-reviews/2026-09-17-modifier-groups-directive-editor.md`
(portal side). Sibling slice:
`ut-cloud/docs/code-reviews/2026-09-17-upsert-category-directive-editor.md`.

## What shipped

- `internal/cloudsync/cloudsync.go`: a new `Hooks.UpsertModifierGroup`
  field, an exported `ModifierGroupOption{Name, PriceDeltaMinor int64}`
  wire struct, a `case "upsert_modifier_group":` in `apply` (requires
  `item_id` + `name`, reads `required` as a bool, `min_select`/`max_select`
  through the existing tolerant `num` helper, options through a new
  `modifierGroupOptions` decoder), and that decoder — the same "JSON array
  inside one string payload field" shape `barcodes` uses, decoding into
  structs instead of strings. A missing/blank `options` field is **no
  options**, not an error; a non-blank field that fails to decode is a real
  directive failure.
- `internal/pages/cloudsync_wire.go`: `cloudUpsertModifierGroup`, wired into
  `buildCloudHooks`. Validates independently of the cloud, checks the item
  exists, dedupes a retried create by case-insensitive name, gates on
  `requirePrimaryDirective`, then
  `NextGroupSortOrderForItem` → `CreateGroup` → `CreateOption` per option →
  `auditCloudDirective("modifier_group", …, "cloud_modifier_group_created")`.
- Tests: `internal/cloudsync/cloudsync_test.go`'s
  `TestApplyUpsertModifierGroup`, and 9 (now 13) tests in
  `internal/pages/cloudsync_wire_test.go`.

**Deliberate scope cut**: CREATE-ONLY, no group id in the payload at all —
identical reasoning to `cloudUpsertCategory`'s, see the paired record.

## Independent review

Opus, fresh context, diff read cold, no access to the Dev's reasoning.

**Constraints re-derived from the real DDL, not from the comments claiming
to mirror them:** `internal/db/migrations/001_init.sql` lines 613-641 —
`item_modifier_groups` carries `CHECK (min_select >= 0 AND max_select >=
min_select)`, `item_modifier_options` carries `CHECK (price_delta_minor >=
0)`. Both sides' validation mirrors these correctly.

**TDD re-verification (the "prove the test isn't a false pass" check).**
Replaced `cloudUpsertModifierGroup`'s body with a stub returning
`("stubbed", nil)` and removed its `buildCloudHooks` wiring, on the
committed tree with a scratch copy for restore. Result: **all 9 new tests
failed**, each on a substantive assertion, not a compile error —
`_CreatesGroupWithOptions` ("unexpected message: \"stubbed\""),
`_ZeroOptionsIsValid`, `_UnknownItemFails`, all three `_InvalidMinMaxFails`
subcases, `_NegativeOptionPriceFails`, `_RefusedOnReplica`,
`_WritesAuditRow`, `_CreateRetryDoesNotDuplicate`, and
`TestBuildCloudHooks_WiresUpsertModifierGroup`. Restored and re-ran: green.
The suite genuinely covers the implementation.

**Idempotency/replica ordering re-derived.** Read `cloudUpsertCategory`
directly above it and traced both orders. The ordering here is **correct**:
the read-only item-existence check and the name dedupe run before
`requirePrimaryDirective`, so a replica replaying an already-applied create
(the group having arrived via `sync_admin_repo.go`'s admin pull —
`item_modifier_groups`, `item_modifier_options` and
`item_modifier_group_links` are all in `adminTables`) reports success rather
than a spurious refusal, while a genuinely new create on a replica still
hits the gate and is refused. That second half is pinned by
`TestCloudUpsertModifierGroup_RefusedOnReplica`. One respect in which the
copy was *not* faithful is finding 1 below.

### Findings

1. **(Fixed) The dedupe counted a DEACTIVATED group as "already applied",
   silently dropping a real create while reporting success.** The scan used
   `ListAllGroupsForItem`, which by its own doc comment deliberately returns
   **every** group for an item, active or not, and matched on name alone.
   `cloudUpsertCategory`, the function this one explicitly claims to
   mirror, filters `c.IsActive && strings.EqualFold(…)` for exactly this
   reason. Consequence: a merchant who retires an "Extras" group at the till
   and later creates a new "Extras" from the portal gets
   `"modifier group Extras already exists on this item"` returned as a
   **successful** directive result — forever, with nothing created and no
   way to tell from the portal that the request was dropped. (A retired
   group is invisible in the sale-time picker, so this is not "it already
   works".) **Fix applied**: `if g.IsActive && strings.EqualFold(…)`.
   Regression test:
   `TestCloudUpsertModifierGroup_DeactivatedGroupDoesNotBlockCreate`
   (creates, deactivates via `UpdateGroup`, re-creates, asserts the message
   is not an "already exists" and that exactly one **active** group with
   that name and the new options exists). Verified failing before the fix.
2. **(Fixed) A partial option write was turned into a permanent silent
   success by the retry dedupe.** `CreateGroup` wraps its own group+link
   inserts in one transaction, but the per-option `CreateOption` calls after
   it are separate, non-transactional statements. The task's framing asks
   whether the pre-validation (which already rejects blank names and
   negative prices before any write) makes this adequate. **It does not —
   real verdict, not a hedge.** Pre-validation removes every *deterministic*
   failure mode, so what remains is an infrastructure error (`SQLITE_BUSY`
   under a concurrent sale write, disk I/O) — plausible on a till, not
   theoretical. On its own that would be an ordinary partial write. What
   makes it worse is the interaction with finding 1's dedupe: directives are
   at-least-once, so the retry finds the half-created group by name and
   reports **"already exists" as a success**, cementing a group that is
   permanently missing options 2..N while telling the merchant it worked.
   Missing options are missing priced up-sells, so this is money-adjacent.
   **Fix applied**: on an option-insert failure the whole create is rolled
   back with `modRepo.DeleteGroup(ctx, groupID)` — which cascades the link
   row and any options already inserted, `foreign_keys` being ON
   (`internal/db/db.go`) — and the error is returned, so the retry recreates
   the group cleanly. A failed compensating delete is logged, never
   swallowed silently. Regression test:
   `TestCloudUpsertModifierGroup_OptionFailureRollsBackTheGroup` uses a
   `BEFORE INSERT … RAISE(ABORT)` trigger to make option 2 of 2 fail
   deterministically, asserts zero group / option / link rows survive, then
   drops the trigger and asserts the retry produces a **complete** 2-option
   group. Verified failing before the fix ("half-created group left behind:
   1 rows"). *(A `ModifierRepo.CreateGroupWithOptions` doing the whole thing
   in one transaction would be tidier still; that is a data-layer change
   beyond this slice and is filed as a follow-up. The compensating delete
   closes the user-visible hole now.)*
3. **(Fixed) A cloud-created group could land in a state the till's own
   admin UI refuses to produce, defeating "required".** The local creator
   (`internal/pages/catalog/handlers.go`, `POST /api/catalog/modifier-group`)
   applies two normalisations this hook did not: `maxSelect < 1 → 1`, and
   `required && minSelect < 1 → minSelect = 1` ("a required group must ask
   for at least one pick"). The cloud form happily submits *required*
   checked with `min_select=0` — its defaults are literally `0`/`1`.
   Why it matters: the sale-time validator
   (`internal/pages/pos_modifiers_api.go:142`) checks **only**
   `MinSelect`/`MaxSelect` and never reads `Required`, while
   `web/ui/partials/modifier_picker.html` renders the "\*" from `Required`.
   So `required=true, min_select=0` produces a group the cashier screen
   marks as required and the server will happily accept zero picks for — the
   merchant's stated intent silently unenforced. **Fix applied**: both
   normalisations mirrored here, before the `min <= max` check (so
   `required` + `max_select=0` is now rejected with a clear message rather
   than clamped into nonsense), with the range check split so a negative
   value and an inverted range give distinct errors. Regression test:
   `TestCloudUpsertModifierGroup_NormalisesRequiredAndMaxSelect`. Verified
   failing before the fix.
4. **Cloud/till validation symmetry: safe end to end, in three layers.**
   Traced the blank-option-name path specifically, as asked.
   `cloudsync.modifierGroupOptions` only *trims* a name and would happily
   hand `{Name: ""}` to the hook — but `cloudUpsertModifierGroup`'s
   pre-write loop rejects `strings.TrimSpace(opt.Name) == ""` **before any
   write starts**, and `data.ModifierRepo.CreateOption` itself refuses a
   blank name as a third backstop. A blank name therefore cannot reach the
   `INSERT`, and after fix 2 cannot leave a partial group behind even if it
   somehow did. Same three-layer story for a negative price (cloud reject →
   hook reject → `CreateOption` reject → DB `CHECK`). The one coverage gap
   was that **no test exercised the blank-name branch of the hook**; added
   `TestCloudUpsertModifierGroup_BlankOptionNameFails`, which also asserts
   nothing was written.
5. **`int64 → int` narrowing on `min_select`/`max_select`: checked, not a
   bug.** `apply` does `int(minSelect)`, which on a 32-bit build (a real
   target — Raspberry Pi) could wrap a huge value. Traced it: any wrap
   produces either a negative or an inverted range, both of which the hook's
   own validation rejects before any write, so no `CHECK`-violating or
   nonsensical row can be produced by it. No change made.
6. **CLAUDE.md compliance: clean.** No SQL query text outside
   `internal/data`/`internal/db` — the hook calls only repository methods,
   and `modifier_repo.go` itself is untouched by this card
   (`guard-data-access.sh` green; the raw SQL in the new tests is in
   `_test.go` files, which the guard exempts by design). Money is integer
   minor units (`price_delta_minor int64`) crossing only the DB/DTO
   boundary, never a float. **The Dev's claim that this is a pure
   sync/audit path with no new user-facing string is correct** — verified,
   not accepted: no template, no locale file and no `T`-lookup is touched;
   the hook's only strings are the directive *result* message returned up
   the sync channel into the portal's directive table (identical to every
   sibling hook: `"created category "+name`, `"price set to %d"`), and
   `guard-i18n.sh` passes with all 1729 template keys resolving and every
   locale matching `en.json`.

## Test quality

Read every new test body in full.

- They assert on **real DB state**, not `err == nil`: the created group's
  `Required`/`MinSelect`/`MaxSelect`, its options' names *and*
  `PriceDeltaMinor` values by name, the `audit_log` row's `entity_type`,
  `entity_id` and `actor_id == "system"`, and — for each rejection path — a
  positive assertion that **no group was created** via
  `ListShopModifierGroups`.
- Duplicate/retry is properly covered:
  `_CreateRetryDoesNotDuplicate` retries with `"Extras"`, `"extras"` and
  `" EXTRAS "`, asserts exactly one group survives, **and** asserts the
  existing group's rules and options were not silently edited by the retry's
  different `required`/`min`/`max`/option payload — i.e. it pins "no-op",
  not merely "no duplicate".
- Replica refusal of a genuinely new create is covered
  (`_RefusedOnReplica`, seeding `sync.primary_url`, asserting no group).
- `TestApplyUpsertModifierGroup` covers the dispatch layer separately with
  hook-call counting: nil hook, three missing-`item_id`/`name` shapes, three
  missing/invalid min/max shapes, the well-formed case (asserting trimming
  and every decoded argument), blank options → zero options, malformed
  options → refused *before* the hook runs, and a hook error becoming the
  directive's failure message.
- Added by this review: `_DeactivatedGroupDoesNotBlockCreate`,
  `_OptionFailureRollsBackTheGroup`, `_NormalisesRequiredAndMaxSelect`,
  `_BlankOptionNameFails`. The first three were each confirmed to fail
  against the pre-fix implementation.

## Verified beyond automated tests

- `gofmt -l .`: no output, exit 0.
- `go build ./...`: exit 0.
- `go test ./internal/cloudsync/... ./internal/pages/...`: all `ok`
  (`cloudsync` 2.710s, `pages` 126.035s, `pages/catalog` 2.445s,
  `pages/common` 3.259s, `pages/itemsnav`, `pages/settingsnav`), exit 0.
- `golangci-lint run ./internal/cloudsync/... ./internal/pages/...`:
  `0 issues.`, exit 0.
- `bash scripts/ci/guard-i18n.sh`: `✓ i18n guard: 1729 template keys
  resolve; all locales match en.json; …`, exit 0.
- `bash scripts/ci/guard-data-access.sh`: `✓ data-access guard: no inline
  SQL outside internal/data / internal/db`, exit 0.
- Mutation/TDD checks described above (hook stubbed → 9 failures; each of
  the three fixes reverted individually → its own regression test failed).

## Verdict

**Safe to merge.** No blocker-class issue survives: findings 1-3 were all
real, verifiable bugs and are all **fixed in this branch with regression
tests that were proven to fail beforehand**. Finding 2 was the most serious
(a half-created group made permanent by the retry dedupe, and option prices
are money), but it is closed, not merely noted. Nothing found touches tax,
existing-data loss or security.

## Deferred / follow-up cards filed

- **`ModifierRepo.CreateGroupWithOptions`** — do the group + link + all
  option inserts in a single transaction in the data layer, and drop this
  hook's compensating `DeleteGroup`. The compensation closes the
  user-visible hole (finding 2) but a real transaction is the correct shape,
  and the local admin creator would benefit from it identically.
- **A sane upper bound on `min_select`/`max_select`** — see the paired
  record's finding 3; the normalisation added here prevents the
  contradictory states, not an absurdly large one.
- **`StoreSnapshot` read-side extension (ADR-0095 Decision 2)**, tracked as
  ut-docs#2354 — required before editing an existing group, attaching an
  existing group to more items, or attaching one to a category can ship
  from the portal.
- **Audit/`requirePrimary` coverage across all cloud-originated catalog
  directives** — the shared follow-up already noted in the categories-slice
  records; this directive is correctly gated and audited, so nothing new is
  owed by this card.
