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

`universaltill/universal-till#1219` (ut-docs#2353, "Gate + audit every cloud
catalog directive on primary-till only") — which independently introduces its
own `requirePrimaryDirective`/`auditCloudDirective` — is confirmed **not
merged** into `main` as of this review; it exists only as an unmerged branch
(commit `2bc0285a`, branch `pr-1219-inspect`, not an ancestor of
`origin/main`). Local `main` was fast-forwarded from `f401ab51` to `309923cf`
(10 commits) to be truly in sync with `origin/main` first; none of those 10
commits touch `internal/cloudsync/**` or `internal/pages/cloudsync_wire*.go`,
so no rebase conflict. **No dedup was needed** — this diff's two helpers are
the only definitions on `main` today. (Whoever lands #1219 later will need to
dedupe against this branch's copies instead — flagged for that future PR, not
actionable here.)

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

## Verdict

**Safe to merge.** All CI-blocking gates relevant to this diff's scope pass;
the TDD claim for the primary-till gate was independently re-verified with a
real revert/restore showing an actual DB mutation on failure; no blockers
found. One item is explicitly deferred rather than fixed:

- **Backlog suggestion:** add a sane upper-bound cap on the barcode list
  length for `set_quick_button_layout` (dispatch-time in `cloudsync.apply()`
  or inside `cloudSetQuickButtonLayout`), and consider having
  `ShortcutsRepo.UpdateOrder` return an affected-row count so a future pass
  can surface partial-success on both the LAN and cloud reorder paths. Not
  created as a card by this Reviewer pass per process (orchestrator's call).

Also flagged for the orchestrator, non-blocking: this review could not use
the pipeline's specified `Agent`/opus-subagent mechanism (tool unavailable to
this execution; CCR `create_session` fallback failed repeatedly with a
permission-mode resolution error) — see "Tooling note" above.
