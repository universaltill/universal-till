# Review: v0.31.10 release note overclaimed my. option sets (ut-docs#4219)

## What shipped

- `web/release-notes/{en,de,ar,fa,tr}/v0.31.10.md`: dropped the "Variants from
  option sets, applied from my." bullet. Only the till-side directive slice
  (ut-docs#4163) shipped in v0.31.10; the ut-cloud endpoint (#4164) and the my.
  UI (#3932) are still open, so nothing a shop owner can use shipped. The
  feature gets announced in the release that ships #3932.
- `RELEASING.md` step 1: a note may credit a surface outside the till (my., the
  website, a plugin) only when that surface's own change has shipped; check
  sibling slices and open `blocked:dep` cards first. The ut-docs devops skill's
  release-notes step already defers to RELEASING.md, so the rule has one home.
- Out of tree: the published v0.31.10 GitHub Release body is edited to drop the
  same bullet after merge. The `release-notes.json` asset is left as published
  (replacing a release asset is riskier than the stale line; every later
  release's bundle carries the corrected note). Tills already on v0.31.10 keep
  the embedded note until they update.

## Guard considered

Not added: notes deliberately carry no card numbers
(`TestBuiltin_NotesAreOwnerLanguage`), so there is no metadata to check a
bullet against its cards offline. The rule sits at the step that drafts the
note from the Done cards.

## Review

Built by a Sonnet dev subagent; reviewed independently by Opus 5.5 (fresh
context, per MODEL-ROUTING for `complexity:easy`).

| Severity | Finding | Outcome |
|---|---|---|
| major (outside diff) | Published Release body still carries the bullet | Fixed after merge via the Releases API (see close-out) |
| nit | RELEASING.md rule is a dense block | Accepted |
| nit | "A cashier can't confirm it" holds for default roles only | Accepted (true by default; migrations 033/052) |

Reviewer checked each locale lost exactly the one bullet with identical
structure (New 2 / Improved 2 / Fixed 2), and that the remaining AI/plugin
bullets describe shipped till behaviour.

## Verified

`guard-release-notes.sh v0.31.10`, `guard-release-notes_test.sh`,
`go test ./internal/releasenotes/...`, `guard-compliance-claims.sh`,
`guard-competitor-naming.sh` — all pass. Copy-only change, no code.

**Verdict:** safe to merge.
