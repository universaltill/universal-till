# Code review: `upsert_category` directive — till side

**Date:** 2026-09-17
**Card:** ut-docs#2323 ("ut-cloud shop panel: categories editor"), a slice of
ut-docs#2289 (Phase A of the cloud shop-panel remote-management epic).
Design: ADR-0095 ("Portal→till configuration — directive-based writes, till
stays authoritative").
**Complexity:** hard (Dev via Fable, review via Opus, per model routing).

## What shipped

- `internal/cloudsync/cloudsync.go`: new `Hooks.UpsertCategory` field and a
  `case "upsert_category":` in `apply()` — same dispatch shape as every
  sibling directive (`set_price`, `create_item`, `set_till_setting`, …).
- `internal/pages/cloudsync_wire.go`: `cloudUpsertCategory` wiring hook,
  calling `data.NewCatalogRepo(d.Db).CreateCategoryWithColor` (empty id) /
  `.UpdateCategory` (id present) — the same repository calls the local admin
  category editor uses.
- `internal/catalogtypes/types.go`: a cross-repo-mirror warning added to
  `ItemColors`' doc comment, naming `ut-cloud/internal/claims.CategoryColors`
  as a hand-kept copy that must change in lockstep (see Findings below).

**Deliberate scope cut, decided before implementation started**: name and
colour only. Modifier-group and kitchen-station attachment are not written
by this directive — the cloud portal cannot offer a safe picker for either
until the read-side `StoreSnapshot` extension (ADR-0095 Decision 2) ships,
since neither is up-synced from the till today. Tracked as a follow-up card
against ut-docs#2289.

## Independent review

Opus subagent (fresh context, did not see the Dev conversation), briefed
with the full diff scope, ADR-0095, and the repo's `CLAUDE.md` rules. It
re-ran `gofmt`, `go build`, and the touched packages' tests independently
(not trusting Dev's self-report), and mutation-tested three invariants by
reverting each in turn and confirming the corresponding test failed, then
restoring:

- Removing the till-side colour-allowlist check →
  `TestCloudUpsertCategory_RefusesOffPaletteColourWithoutWriting` failed.
- Disabling the active-name create dedupe →
  `TestCloudUpsertCategory_CreateRetryDoesNotDuplicate` failed.
- (ut-cloud side) Disabling the cloud's own colour allowlist →
  `TestQueueDirectiveUpsertCategory/off-palette_colours_are_refused` failed.

No test in either repo would survive a no-op implementation.

### Findings

1. **(Fixed, this repo) Palette mirror had no cross-repo warning.**
   `ut-cloud`'s `CategoryColors` hand-copies this repo's `ItemColors()` (same
   eight hex values, same order) with no mechanical guard tying them
   together. `ItemColors`' own doc comment asserted "Defined once, here, so
   the picker UI and the server-side allowlist can never drift apart" — this
   PR made that false without saying so. Fixed by adding a "CROSS-REPO
   MIRROR" paragraph to the comment naming the exact ut-cloud symbol and the
   failure mode. **Not fixed (out of scope, follow-up filed):** no
   mechanical guard exists yet; `ut-cloud` already runs
   `manifest-contract-guard` (checks out this repo to verify
   `CanonicalManifest` mirrors `plugins.Manifest`) — the same pattern applied
   to this palette is the natural fix, tracked as a follow-up rather than
   built into this slice.
2. **Colour validated independently on both sides** — confirmed by reading
   the code and by the mutation test above. The till does not trust the
   cloud's own filtering (`internal/pages/cloudsync_wire.go`'s
   `catalogtypes.ValidItemColor` call is independent of `ut-cloud`'s
   `queueDirective` check), per this repo's "validate all external input"
   rule. One asymmetry, harmless direction: the till trims before validating
   (`strings.TrimSpace`), the cloud form does not, so a leading/trailing
   space is refused earlier by the cloud than it would be by the till. Left
   as-is (nit, not a defect).
3. **Retry/idempotency scan is correct.** The empty-id path scans
   `ListCategoriesForAdmin` for an active, case-insensitive name match before
   creating — verified against the schema (`categories.name` has no UNIQUE
   constraint) and against the precedent `cloudCreateItem` already sets
   ("existing active item with the same name counts as success"). An
   inactive category sharing the new name does not block a fresh active
   create, which is correct: the portal cannot reactivate an inactive row,
   and a subsequent retry of that same create correctly dedupes against the
   now-active result.
4. **Missing `requirePrimary` gate and audit-row write — pre-existing, not a
   new regression.** The local admin category editor
   (`categories_page.go:408/437`) gates writes on `requirePrimary` and logs
   an audit event; this new directive hook does neither. Verified this
   matches every sibling catalog directive (`cloudCreateItem`, `cloudSetPrice`,
   `cloudRenameItem` — none of them gate or audit either), so this is a
   pre-existing gap in the directive layer as a whole, not something this PR
   introduces. **Follow-up card filed** rather than fixed here, scoped to all
   catalog directives together rather than singling out this one.
5. **The category-edit path is effectively unreachable from the portal UI as
   originally shipped** — the catalog snapshot carries items only, never a
   category id or name, and the till's own Categories page never displays a
   row's id to an operator either. There was no way for a merchant to
   discover an id to edit by. Combined with `color`'s absent-vs-empty
   ambiguity (both read as `""` on the till, both mean "clear"), the original
   form risked a merchant renaming a category and silently losing its
   colour with no visible warning and no way back. **Fixed on the ut-cloud
   side**: the portal form is now create-only (no `id` field at all) until a
   category list is actually synced down (ADR-0095 Decision 2); the
   till-side hook and its update path are unchanged and remain ready for
   that future slice. See the paired `ut-cloud` review record for the exact
   change.

## Verified beyond automated tests

- `gofmt -l .`: clean.
- `go build ./...`: clean.
- `go test ./internal/cloudsync/... ./internal/catalogtypes/...`: pass
  (re-run after the review fix landed).
- `go test ./... -race`: one unrelated failure,
  `internal/plugins.TestCheckForUpdatesFindsNewerVersion` (600s timeout under
  `-race` load). Confirmed pre-existing/environmental, not caused by this
  diff: the diff touches no file under `internal/plugins` or `internal/db`,
  and the test passes cleanly in isolation (`go test ./internal/plugins/...
  -run TestCheckForUpdatesFindsNewerVersion -race`, 3.02s, PASS).
- `golangci-lint run ./...`: 0 issues (Dev's own run; re-confirmed on the
  touched packages).
- `guard-data-access.sh`, `guard-i18n.sh`: pass (no SQL outside
  `internal/data`/`internal/db`; no UI/locale surface touched in this repo).

## Verdict

**Safe to merge** after finding 5's fix (landed on the paired `ut-cloud`
PR). No blocker-class issue (money/tax/data-loss/security) in either repo's
diff. Findings 1 and 4 are tracked as follow-up cards rather than blocking
this slice.

## Deferred / follow-up cards filed

- Extend `manifest-contract-guard`'s pattern to catch
  `ItemColors`/`CategoryColors` palette drift between the two repos.
- Audit/`requirePrimary` gap across all cloud-originated catalog directives
  (not specific to `upsert_category`).
- Category id/name surfacing (till-side Categories page + `StoreSnapshot`
  read-side extension, ADR-0095 Decision 2) — needed before a real edit UI
  can ship; modifier-group and kitchen-station attachment on categories
  depend on the same read-side work.
