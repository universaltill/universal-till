# 2026-09-11 — Package doc: uislot's "further slots" claim was stale (ut-docs#2075)

## What shipped

`internal/uislot/slot.go`'s package doc comment still described Settings
groupings as a future follow-up ("Further slots (settings groups) attach
the same way...") even though `SettingsSlot` (ut-docs#1913) shipped a
while ago — the const, `settingsSpec`, and the rest of the settings-slot
machinery are already in this same file. The comment simply never caught
up with the code.

Doc-comment-only change, no behavior change:

- Names all four shipped slots (Menu, Items, Rail, Settings) as attaching
  "the same way," instead of implying Settings was still pending.
- Notes that no fifth slot candidate is identified today: ADR-0091
  (2026-09-11, Accepted) considered the catalog list's own presentation
  (table vs. card grid) as a possible fifth entry and declined it — that
  choice is "one view, one axis, one value at a time," not a list of
  keyed, orderable destinations like the four shipped slots, so it stays
  a core-only default reachable through the theme seam instead.

## Independent review (fresh-context Sonnet subagent, isolated worktree)

First pass (against my uncommitted working-tree edit) came back empty —
`Agent(isolation: "worktree")` checks out a separate worktree that can't
see uncommitted changes in the main checkout, so there was nothing there
to review. Fixed by committing to a branch, pushing, and re-running the
review against `origin/fix/2075-uislot-package-doc-stale` vs. `origin/main`.

Second pass confirmed:
- The diff is genuinely comment-only: `gofmt -l internal/uislot/slot.go`
  empty, `go build ./...` clean, `go test ./internal/uislot/...` green;
  every other line in the 975-line file (consts, `CoreSettings`,
  `settingsSpec`, `Resolve`, `FindConflict`, …) byte-identical to `main`.
- Every factual claim checked out: `SettingsSlot` exists with the
  `ut-docs#1913` reference exactly as claimed; ADR-0091 does decline a
  fifth slot for catalog-list presentation, using the literal phrase "one
  view, one axis, one value at a time" — the new comment's paraphrase
  matches almost verbatim.
- **Real finding, fixed before this push:** the branch as first pushed
  was accidentally stacked on top of `fix/2091-vary-hx-request` (created
  from that branch's own checkout without switching back to `main`
  first), so it carried the unrelated Vary-header fix's commits too.
  Fixed by `git rebase --onto origin/main` to isolate this branch to just
  the one doc-comment commit, force-pushed, and re-verified
  build/test/gofmt clean on the rebased branch.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go test ./internal/uislot/...` clean
  on the final, rebased branch.
- No i18n/help-topic/compliance surface touched (backend package doc
  only, no user-facing string, no route, no template) — the usual guards
  don't apply to this diff and weren't run for that reason, not skipped.

## Not verified

Nothing outside normal scope — this is a doc-comment correction with no
runtime behavior, no template, and no i18n key.
