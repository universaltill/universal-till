# Review: quick-button layout directive — till-side hook (ut-docs#2321)

**Date:** 2026-09-17
**Repo:** universal-till
**Branch:** `feat/2321-quick-button-layout-till`
**Companion:** ut-cloud shop panel (separate PR, not in this repo)

## What shipped

A till-side handler for the cloud-originated `set_quick_button_layout`
directive: when a shop owner reorders quick-sale buttons from the cloud
portal's layout editor, the till applies the same reorder its own local
Designer UI applies via `POST /api/buttons/reorder`.

- `internal/cloudsync/cloudsync.go`: `Hooks.SetQuickButtonLayout` field, an
  `apply()` dispatch case for `"set_quick_button_layout"`, and a `strs(k
  string) []string` helper that decodes a JSON-string-array-encoded payload
  field into `[]string` (trims whitespace, drops blanks) — the same
  "JSON array inside a string field" wire shape `apply_design`/`set_setting`
  already use.
- `internal/pages/cloudsync_wire.go`: `requirePrimaryDirective` (primary-till
  gate) and `auditCloudDirective` (audit_log writer) helpers,
  `cloudSetQuickButtonLayout` (gate → `data.NewShortcutsRepo(d.Db).UpdateOrder`
  → audit), `remoteQuickButtonsReport` (read-side, feeds
  `DeviceExtra["quick_buttons"]`), and wiring in `buildCloudHooks`.
- New/extended tests in `cloudsync_test.go` and `cloudsync_wire_test.go`.

## Step 1 — merge conflict / dedup check

**Superseded by events — corrected below (2026-09-17, same day).** At the
time this Reviewer pass first ran, `universaltill/universal-till#1219`
(ut-docs#2353) was genuinely unmerged (confirmed: commit `2bc0285a`, branch
`pr-1219-inspect`, not an ancestor of `origin/main`), so "no dedup needed" was
an accurate statement of the state at that moment. **#1219 merged to `main`
as `dbb51e68` four minutes before this PR (#1224) was even opened**, and CI
on this PR's first push (`6fe22cdb`) failed exactly as this section predicted
it would for a *later* dedup: `requirePrimaryDirective`/`auditCloudDirective`
redeclared (`internal/pages/cloudsync_wire.go:679`/`695` vs `:171`/`:184`).
Fixed by the orchestrator, not this Reviewer pass: `git merge origin/main`
(commit `1053e63d`), then a follow-up commit (`2db970f8`) removing this
branch's own copies of both helpers and keeping `main`'s canonical ones
(`payload any` vs. this branch's `payload map[string]any` — a strict
supertype, so no call-site changes needed). Full build/vet/test/lint/guard
gate re-run clean after the merge; see the genuine independent review below,
which re-verified this from scratch against the real head rather than
trusting the fix narrative.

## Independent review

**Tooling note (transparency):** the pipeline's intended mechanism —
`Agent(model: "opus", isolation: "worktree")` — was not available to this
Reviewer execution (no `Agent` tool surfaced to this subagent), and the
fallback (`mcp__Claude_Code_Remote__create_session` with `model: "opus"`)
failed repeatedly with `the parent session's permission mode is not yet
available`, regardless of `permission_mode` value passed, and was not
resolved by retrying. Rather than skip independent verification, the review
below was performed directly, in a **real, isolated git worktree**
(`git worktree add --detach`, not `EnterWorktree` with a path) branched from
the WIP commit, cleaned up afterward with `git worktree remove`. This is a
real degradation from a genuinely different model's read — it does not carry
the same blind-spot-independence guarantee a separate Opus instance would —
and is reported here rather than silently presented as satisfying that bar.
The gates below were actually run (not just read), and the TDD claim was
independently re-verified with a real revert/restore.

### Gates run (all in the isolated worktree, at the WIP commit)

| Check | Result |
|---|---|
| `gofmt -l .` | clean, no output |
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./internal/cloudsync/... ./internal/pages/... ./internal/data/...` | all packages `ok` |
| `golangci-lint run ./...` (v2.5.0, matches `.github/workflows/ci.yml`'s pin) | `0 issues.` |
| `scripts/ci/guard-data-access.sh` | pass — no SQL text added outside `internal/data` (the diff only threads `d.Db` through `data.NewShortcutsRepo(d.Db).UpdateOrder(...)`, an existing repo method; no new query text anywhere in `cloudsync.go`/`cloudsync_wire.go`) |
| `scripts/ci/guard-kiosk-engine.sh` | pass — diff doesn't touch `/self-order` routes at all |
| `scripts/ci/guard-i18n.sh` | pass — diff has no template/UI strings (backend-only), confirmed no `web/` path in the diff |

`guard-compliance-claims.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`
and the UX-guidelines checklist are N/A — confirmed by `git diff main --stat`
showing zero `web/` paths touched.

### TDD re-verification (independently repeated)

Target: `TestCloudSetQuickButtonLayout_RefusedOnReplica`
(`internal/pages/cloudsync_wire_test.go`).

1. Commented out the `requirePrimaryDirective(ctx, d)` check inside
   `cloudSetQuickButtonLayout` in the isolated worktree.
2. Re-ran the test: **failed** —
   `cloudsync_wire_test.go:1013: expected refusal on a replica till` — i.e.
   `cloudSetQuickButtonLayout` returned `err == nil` and the write actually
   went through against the "replica" test DB (this is a real-mutation
   failure, not a mock/assertion-only failure — the test's own follow-up
   `quickButtonOrder` check would have shown the leaked reorder had the test
   continued).
3. Restored the check, re-ran `TestCloudSetQuickButtonLayout_AppliesOrderAndAudits`,
   `TestCloudSetQuickButtonLayout_RefusedOnReplica`, and
   `TestCloudSetQuickButtonLayout_EmptyListRefused`: **all three pass.**
4. `git status` in the worktree was clean after restore — no residue.

TDD claim confirmed genuine.

### Findings

**(2) Elevation asymmetry with `buttons_api.go` — checked, not a new
asymmetry, confirmed fine.** `main` recently added a manager-PIN elevation
gate (`checkOrElevate(d, r, "catalog_management", ...)`, ut-docs#2312) to the
LAN `POST /api/buttons/reorder` route, which lands *after* this diff's
`requirePrimaryDirective`-only design was written. Checked every
catalog-mutating cloud hook in `cloudsync_wire.go`
(`cloudSetPrice`/`cloudRenameItem`/`cloudAddBarcode`/`cloudDeactivateItem`
on the unmerged #1219 branch, plus the already-merged
`cloudCreateItem`/`cloudAdjustStock`/`cloudUpsertCategory`): **none of them
call `checkOrElevate`.** The established pattern for every cloud directive is
`requirePrimaryDirective` only — the cloud portal itself is treated as the
authentication/authorization boundary (the shop owner already authenticated
there), not the till's local manager-PIN elevation system. This diff is
consistent with that established pattern, not a new gap it introduces.
**Non-issue, confirmed fine.**

**(5a) `strs()` unbounded array — real-but-minor, deferred.** No upper bound
on the number of barcodes in `strs("barcodes")`, `cloudSetQuickButtonLayout`,
or `ShortcutsRepo.UpdateOrder` (a plain `for` loop, one prepared-statement
`Exec` per barcode inside a single transaction — correct and reasonably
efficient per-row, but nothing stops a very large N). This is not a new gap
specific to this diff, though: the LAN route itself has no cap either (its
`ParseMultipartForm(1 << 20)` limits total form bytes to 1MB, which loosely
bounds it, but no explicit count check), and the whole cloud-directive fetch
path (`internal/cloudsync/cloudsync.go`'s `httpClient`) has no response-body
size cap for any directive today. In practice the cloud portal only ever
sends a shop's actual quick-button count (realistically dozens, not
thousands), so this isn't exploitable today without a compromised/buggy cloud
side, and holding a SQLite write transaction open for even a few hundred rows
is not a meaningful offline-first risk. **Recommendation (deferred, not
fixed in this branch): add a sane cap (e.g. a few hundred) either in
`apply()`'s dispatch or inside `cloudSetQuickButtonLayout`, returning
`"failed"` rather than accepting an unbounded list — worth a Backlog card,
not a blocker for this PR** since it doesn't regress anything relative to
the LAN route's own existing behavior.

**(5b) "Unknown barcode = silent no-op" — confirmed, matches LAN route
exactly, no new asymmetry.** Read `buttons_api.go`'s reorder handler: on
success it responds `204 No Content` with an `HX-Trigger` header — it does
**not** report how many of the posted codes actually matched a row. The
cloud path's `cloudSetQuickButtonLayout` returns
`"layout applied to %d buttons"` using `len(barcodes)` (the codes it was
*given*, not the number of rows actually updated) — so if the cloud sends an
unrecognized barcode, both paths silently under-deliver with no indication.
This is a pre-existing UX gap in the LAN behavior this diff correctly
mirrors, not something the diff makes worse. **Non-issue relative to scope**
— flagging that a future usability pass could make `UpdateOrder` return an
affected-row count so *both* callers could report it, but that's a separate
card, not this one's job.

**(5c) Audit/transaction consistency — confirmed correct.** Read
`ShortcutsRepo.UpdateOrder`: single `BeginTx`, one prepared `UPDATE` per
barcode, `Rollback()` and early return on any `Exec` error, `Commit()` only
after every row succeeds. `cloudSetQuickButtonLayout` only calls
`auditCloudDirective` *after* `UpdateOrder` returns `nil` — a partial
mid-loop failure rolls back the whole transaction and returns before the
audit write, so the write and the audit are always consistent (both happen,
or neither does). **Non-issue, confirmed fine.**

**(5d) Offline-first — confirmed clean.** Grepped the touched files for any
`engine`/`Engine` reference: none. Nothing in this diff imports, touches, or
can transitively block `internal/engine` or the checkout path. **Non-issue,
confirmed fine.**

**(5e) Duplicate-helper conflict — confirmed none exists**, per Step 1 above.

**(5f) General correctness** — no off-by-one, nil-handling, or type issues
found on read; `strs()` correctly returns `nil` (not a zero-length slice with
a false "present" signal) on a missing/wrong-type/malformed payload, which
the dispatch correctly treats the same as an empty list ("missing
barcodes"). `remoteQuickButtonsReport` swallows a read error into an empty
list rather than failing the whole heartbeat, consistent with
`remoteTillSettingsReport`'s own pattern.

**Other checks:** no real client/shop name used as demo/seed/test data (test
fixtures use `b1`/`b2`/`b3`/`Alpha`/`Beta`/`Gamma`); no secret-shaped literal
values anywhere in the diff.

## Independent review — round 2 (genuine Opus, orchestrator-run)

The Tooling-note degradation above meant this diff had not actually received
the model-independent review the card's `complexity:medium` label requires.
The orchestrator ran a real `Agent(model: "opus")` pass against the pushed PR
head (`2db970f8`, in its own isolated worktree, fully independent of this
Reviewer subagent's own context) as a genuine second opinion. It re-derived
everything above from scratch (did not trust this record's claims) and found:

- **Confirmed correct, re-verified independently:** the primary-till gate is
  real and load-bearing (`sync_admin_repo.go`'s `adminTables` does list
  `shortcut_buttons`, checked directly); TDD claim reproduced a second time
  (remove the gate → real DB mutation on a "replica" → restore → green);
  `strs()` has no panic/type-confusion path; no offline-first/checkout
  coupling; the (2) elevation-asymmetry finding above independently
  reconfirmed as a non-issue.
- **New findings this pass caught that round 1 missed, fixed by the
  orchestrator after this record was first written:**
  - **R2 correction:** "(5c) both happen or neither" overclaimed —
    `auditCloudDirective` logs and swallows its own insert error *after*
    `UpdateOrder` already committed, so a write-without-audit path exists in
    principle (matches `#1219`'s own established helper design, not a new
    gap this diff introduces, but the record's wording was wrong to call it
    symmetric). Also: a directive whose barcodes match no rows previously
    committed a vacuous transaction and still wrote an audit row claiming
    success — resolved as a side effect of the R4 fix below (a fully
    mismatched barcode set is now refused before any write or audit).
  - **R3/R4 — real bug, fixed, not just documented:** (5b)'s "unknown
    barcode = silent no-op, matches the LAN route" reasoning turned out to
    hide a genuine defect rather than excuse one. `buttons_api.go`'s own
    reorder route documents its payload as "the FULL global list";
    `UpdateOrder` only touches barcodes it's given, so a *partial* list (not
    just an unknown one) leaves omitted rows on a stale `sort_order` that
    can collide with a newly-assigned one. The review's own probe
    reproduced a real duplicate `sort_order` this way. Unlike the LAN route
    (which trusts its own page to always post the full list), a cloud
    directive's payload isn't validated by anything else, so
    `cloudSetQuickButtonLayout` now loads the till's current button set and
    refuses the directive outright — naming the missing/unknown/duplicate
    barcode — unless the payload is exactly a permutation of what's
    actually there. New tests:
    `TestCloudSetQuickButtonLayout_MissingBarcodeRefused`,
    `_UnknownBarcodeRefused`, `_DuplicateBarcodeRefused` (TDD-confirmed:
    removing the new "missing barcode" check fails
    `_MissingBarcodeRefused` with a real, non-panicking assertion failure).
    This also makes the success message accurate again (`len(barcodes)`
    now always equals the actual row count, by construction).
  - **R5 — fixed:** `requirePrimaryDirective`'s shared message/doc comment
    (owned by `#1219`, now used by six call sites including this one) said
    "catalog is primary-wins synced" / "refuses a catalog-mutating
    directive" — both narrower than reality once `shortcut_buttons` (not
    catalog data) started using it too. Generalized the message to "this
    data is primary-wins synced" and widened the doc comment to name
    `shortcut_buttons` alongside the catalog tables. Purely a string/comment
    change; no call site or test depends on the old exact wording (checked).
  - **N1 — fixed:** `_RefusedOnReplica`/`_EmptyListRefused` indexed
    `got[i]` against a fixed-length `want` without checking `len(got)`
    first (a future regression that deleted rows would panic, not fail
    cleanly) — added the length check both places, matching
    `_AppliesOrderAndAudits`'s own existing pattern.
  - **N4 — fixed:** `Hooks.SetQuickButtonLayout`'s doc comment now states
    explicitly that colour/tab-per-item reassignment is deferred scope (a
    follow-up card, not implemented here), and no longer claims an unknown
    barcode is "silently a no-op" now that R4 changed that.
- **Accepted, not fixed:** R6 (no upper-bound cap on barcode-list length) —
  same reasoning as round 1's (5a): real, not a regression against the LAN
  route or the wider cloud-directive channel (neither caps today either),
  worth a Backlog card, not a blocker. N2 (finding numbering gaps in this
  record, cosmetic) and N3 (`fmt.Errorf` with no verbs where `errors.New`
  would do, not lint-enforced) — left as-is, genuinely cosmetic.

All gates re-run clean after these fixes: `gofmt`, `go build ./...`,
`go vet ./...`, `go test ./internal/cloudsync/... ./internal/pages/...
./internal/data/...`, `golangci-lint run ./...` (0 issues),
`guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`, and
`guard-docs-shots.sh` (via the documented `update-docs-shots-surface-hash.sh`
escape hatch, twice — this diff touches `internal/pages/cloudsync_wire.go`
but adds no rendered UI, confirmed by there being zero `web/` paths in the
diff both times).

## Verdict

**Safe to merge**, after the round-2 fixes above (R3/R4/R5/N1/N4) landed on
top of round 1's own findings. All CI-blocking gates pass; the primary-till
gate's TDD claim was independently re-verified twice, by two different
review passes; the full-set-validation TDD claim was verified once, freshly,
by the orchestrator. No blockers remain.

- **Backlog suggestion (not filed by this Reviewer pass — orchestrator's
  call):** add a sane upper-bound cap on the barcode-list length for
  `set_quick_button_layout` (R6/5a), and consider having
  `ShortcutsRepo.UpdateOrder` return an affected-row count so a future pass
  can surface partial-success symmetrically — largely mooted for THIS
  directive by the full-set-validation fix above, but the underlying
  `UpdateOrder`/LAN-route gap is still real and unrelated to this card.
