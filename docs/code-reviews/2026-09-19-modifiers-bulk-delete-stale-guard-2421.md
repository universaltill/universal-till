# Review: stale-count guard on bulk-delete-unassigned modifier groups (universaltill/ut-docs#2421)

**Card:** Follow-up from the independent review of ut-docs#2406 (finding 3,
non-blocking, logged as ut-docs#2421). `/modifiers`' "Delete all unassigned
groups" button confirms a count server-rendered on the *previous* GET; if
another operator (a second tab, another till, or simply creating a new
group — unassigned by definition) changes what's unassigned before the
click, the handler deleted however many groups were *actually* unassigned
at execution time, silently exceeding what the confirm dialog named, with
no undo.

**Complexity:** medium. Dev at Sonnet (inline), review at Opus (fresh
subagent, isolated worktree).

## What shipped

- `internal/data.ModifierRepo.CountUnassignedGroups(ctx) (int, error)` — a
  `SELECT COUNT(*)` against the exact same `WHERE id NOT IN (...) AND id
  NOT IN (...)` clause `DeleteUnassignedGroups` already uses, so the two
  can never independently drift on what "unassigned" means.
- `web/ui/pages/modifiers.html`: the bulk-delete button now carries the
  count it displayed via `hx-vals='{{ jsonVals "expectedCount"
  .UnassignedCount }}'` — the same bare-button, no-surrounding-`<form>`
  round-trip pattern already used throughout this page and the rest of the
  codebase (e.g. `suggestions.html`, `basket.html`).
- `internal/pages/catalog/handlers.go`: the
  `/api/catalog/modifier-group/delete-unassigned` handler now re-checks a
  *fresh* `CountUnassignedGroups` immediately before deleting, after the
  existing `requireCatalogManagement`/`requirePrimary` gates. A mismatch —
  including a missing or unparsable `expectedCount`, never trusted as "no
  check needed" — refuses with 409 through the same
  `renderModifierMutationResult` dispatch every other refused mutation on
  this page already uses (mirrors ut-docs#2046's stale-attach-picker 409).
- 1 new i18n key (`modifiers.delete_unassigned_stale`) × 4 core locales
  (en/ar/fa/tr). `web/help/en/catalog.md` updated with one sentence noting
  the refusal behaviour.
- Tests (`modifiers_bulk_delete_unassigned_2406_test.go`):
  `TestModifierGroupDeleteUnassigned_StaleCountRefused` (stale count → 409,
  nothing deleted; missing count → 409, nothing deleted); existing
  `TestModifierGroupDeleteUnassigned_RemovesOnlyUnassigned` updated to pass
  the now-required `expectedCount`; existing
  `TestModifierGroupDeleteUnassigned_RefusedOnReplica` updated to pass a
  *correct* `expectedCount` and assert on the replica-gate notice text
  specifically, so it still pins the replica gate rather than accidentally
  passing for the new check's reason instead.

## Independent review (Opus, isolated worktree)

**Verdict: PASS-WITH-NITS → all actionable nits resolved before merge.**

1. **Manual gap, fixed.** `web/help/en/catalog.md` documented the
   bulk-delete button but not the new refusal — CLAUDE.md requires the
   manual to ship in the same branch as a behaviour change a shop owner
   can see. Added one sentence to the existing bullet;
   `guard-help-drift.sh` confirms this didn't add or remove a
   heading/bullet, so no locale drift was introduced (ar/de/fa/tr's
   `catalog.md` don't document the #2406 feature at all yet — pre-existing
   gap, unrelated to this card, not widened by it).
2. **Replica test weakened, fixed.** The test posted an empty form, which
   the new missing-count branch would *also* 409 on — no longer proof the
   replica gate specifically is what refused it. Changed to post a
   *correct* `expectedCount` and assert the response names "primary till",
   so the test still pins the right gate.
3. **Residual TOCTOU window, accepted, not fixed.** The count-then-delete
   is two separate statements, not one transaction — the race shrinks from
   minutes (previous behaviour) to microseconds rather than closing
   entirely. Matches the issue's own acceptance criteria (a count check,
   not a broader transactional rewrite) and the issue's own framing ("not
   a data-safety issue, just a UX/surprise one"); logged here rather than
   silently dropped.
4. **Count equality vs. list identity, accepted, not fixed.** An
   equal-sized swap (one group reassigned elsewhere, a different one
   created) passes the count check and deletes a group the operator never
   saw named. Strictly stronger would be re-sending the actual group IDs
   (as ut-docs#2046's attach-picker does with a list), but the issue
   explicitly asked for a count-based guard; noted as a possible follow-up
   if this class of near-simultaneous edit turns out to matter in
   practice.

Verified independently by the reviewer, not taken on trust:

- TDD claim: reverting `internal/data/modifier_repo.go` and
  `internal/pages/catalog/handlers.go` to `main` makes
  `TestModifierGroupDeleteUnassigned_StaleCountRefused` fail with the real
  over-delete (`want 409, got 200`), not a shallow assertion; restoring
  passes again.
- `CountUnassignedGroups`'s WHERE clause is character-identical to
  `DeleteUnassignedGroups`'s; the page's own Go-side `UnassignedCount`
  tally can't disagree with either in an unsafe direction because FKs are
  enforced (`internal/db/db.go`) — no dangling link row can exist.
- Gate ordering: `requireCatalogManagement` → `requirePrimary` → count
  check, confirmed by posting a *correct* `expectedCount` against a
  replica and still getting 409 with the replica-gate's own notice text.
- `hx-vals`'s int-valued JSON field parses the same way every other
  `r.Form.Get(...)`-read bare-button field on this page already does —
  confirmed against precedent (`suggestions.html`'s `"qty" 1`).
- Scope: `web/ui/pages/modifiers.html` diff touches only the bulk-delete
  button; the single-group delete button is untouched, per the issue's own
  acceptance criteria.

## Verified beyond automated tests

- Full gate: `gofmt -l` (clean), `go build ./...`, `go vet ./...`, full
  `go test ./...` (repo-wide, green), `golangci-lint run` (0 issues on
  touched packages).
- CI guards: `guard-i18n.sh`, `guard-data-access.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`
  (no new drift — pre-existing baselined entries only, `catalog`/`modifiers`
  topic not among them).

## Deferred / follow-up

- List-identity guard (group IDs, not just a count) — not requested by the
  issue; worth reconsidering only if a real near-simultaneous-edit report
  surfaces.
- `web/help/{ar,de,fa,tr}/catalog.md` translation of the #2406 bulk-delete
  feature itself (not just this card's one-sentence addition) remains
  outside this card's scope — pre-existing gap tracked separately.

**Safe to merge.**
