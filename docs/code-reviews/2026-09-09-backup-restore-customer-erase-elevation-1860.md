# Step-up manager-PIN gate for backup restore and GDPR customer erase (ut-docs#1860)

- **Date**: 2026-09-09
- **Card**: ut-docs#1860 (`p2`, `security`, `pilot:germany`, `complexity:medium`)
- **Lane**: `lane:cloud-24`
- **ADR**: ADR-0087 (addendum; no new decision — extends the existing `checkStepUp` mechanism to two more call sites the ADR explicitly named as follow-up)
- **Repos touched**: `universal-till`, `ut-docs`
- **Model routing**: Dev = inline (Sonnet session), independent review = Opus subagent (isolated worktree)

## What shipped

Two of the Settings → Data card's destructive/PII actions were left on weaker gates when ut-docs#1841 (ADR-0087) shipped `checkStepUp` for the other four:

- `POST /api/backup/restore` — a typed `RESTORE` word, no PIN, despite discarding the entire live database.
- `POST /api/data/customers/erase` — no confirmation at all (a GDPR erasure, one click).

Both now use `checkStepUp`, exactly like the original four:

- `internal/pages/backup_api.go` — the restore handler's typed-word check replaced with `checkStepUp("data_management", ...)`; a new `auditElev` closure records the audit row with dual attribution (`InsertAuditElevated`/`InsertAudit`, branched on `blockedActorID`) instead of the flat `audit()` helper the file's other endpoints use. The elevation summary interpolates the backup's own filename (which already carries its date).
- `internal/pages/data_api.go` — the erase handler gained `checkStepUp`; the elevation summary interpolates the customer's **id**, deliberately not name/phone/email — those are exactly the fields about to be erased, so the confirmation step (and the audit trail) must not put them on screen a second time.
- `internal/data/pos_repo.go` — `EraseCustomer` gained a `blockedActorID` parameter, mirroring `ResetTransactionHistory` (`internal/data/reset_archive_repo.go`): the dual-attribution audit write happens inside the same transaction as the mutation, not a second write from the handler layer.
- `web/ui/pages/settings.html` — the typed-word `<input>` removed from the restore form (an htmx form, so the existing OOB-swap elevation dialog needs no extra JS); the customer-erase click handler moved off a bare `fetch()` + `window.confirm()` onto `window.utPostWithElevation`, same pattern `cat-cleanup-btn` already established.
- `web/locales/{en,ar,fa,tr}.json` — two new `elevation.summary.*` keys added; `settings.backup.confirm_required`/`confirm_placeholder` removed (now unused).
- `web/help/{en,ar,de,fa,tr}/backups.md` — restore section rewritten to describe the PIN instead of typing `RESTORE`.
- Screenshots regenerated (`make docs-shots`) since `settings.html` changed.
- `ut-docs/adr/0087-...md` — a short addendum closing the loop that ADR's own Consequences section explicitly left open for this card.

## BA verification note

The card's acceptance criteria described the erase endpoint as `POST /api/data/customers/{id}/erase`. The real, shipped route is `POST /api/data/customers/erase` with `id` as a form value, not a path parameter — verified against `internal/pages/data_api.go` before implementing, not assumed from the card text. The card also said the customer reference should use "the same non-identifying reference the UI already uses" — no such existing convention was found in the codebase (grepped for it); the customer's own id was chosen as the reference instead, since it carries no PII by itself, unlike name/phone/email.

## Findings

### Found by independent review (Opus, isolated worktree, `de4e0ae`)

1. **[Fixed, binding-rule] `web/help/*/display.md` item 15 not updated.** `backups.md` was correctly rewritten in all 5 locales, but `display.md`'s own item 15 (Settings → Data → Erase a customer) still described the pre-diff one-click flow, with no mention of the new PIN — while items 12–14 immediately above it, updated by #1841, all say a PIN is required. Left as-is, the manual would have actively implied erase was the one action on that card that *doesn't* need a PIN. Fixed: one sentence appended to item 15 in all 5 locales; screenshots regenerated (text-only change, but `guard-docs-shots.sh` hashes topic markdown content, not just rendered pixels).
2. **[Fixed, nit] Stale test comment** in `data_api_test.go` referenced a test name (`TestEraseCustomer_ElevatedByApprover_ErasesAndRecordsApprover`) that was renamed away during drafting and no longer exists. Fixed to point at the real location (`TestDataManagementEndpoints_ValidPIN_AuditRecordsApprover`'s `customer-erase` case).
3. **[Fixed, nit] Backup name validated after the PIN, not before.** `POST /api/backup/restore` rendered the elevation prompt (and would have let an approver spend a PIN) even for a malformed/nonexistent backup name, unlike `download`/`save-copy` in the same file which validate `db.ValidBackupName` up front. No security impact (`StageRestore` validates internally; the name is `html/template`-escaped into the summary), but a wasted PIN prompt for input that was always going to fail. Fixed: `db.ValidBackupName` check moved before `checkStepUp`, with a new regression test (`TestRestoreBackup_InvalidNameRejectedBeforeElevation`).
4. **[Accepted, deferred] The `restore_staged` audit row lives in the database about to be replaced.** Pre-existing property of this handler (the `restart-now` handler a few lines below already declines to audit for the same reason), not introduced by this diff — noted as a follow-up rather than fixed here, since fixing it is a design question (where does a "the DB is about to be replaced" audit trail actually live) outside this card's scope.
5. **[Not fixed, judged unnecessary] Table-driven audit test doesn't explicitly re-assert the customer row is gone** after the elevated-erase case. Not a real gap: the audit row is only written after `RowsAffected() > 0` inside the same transaction, so its presence transitively proves the delete, and the repo-layer mutation itself is covered directly by `internal/data/reset_test.go`'s `TestEraseCustomer`.

No blocker-class findings (money/tax/data-loss/security-bypass). The review's TDD re-verification (see below) confirmed the actual gate — not just its test — blocks the mutation.

## Verified beyond automated tests

- **TDD re-verification, both new gates** (isolated worktree, `de4e0ae`): reviewer independently reverted each `checkStepUp` refusal branch in turn and confirmed the corresponding test fails with the mutation actually happening (`TestEraseCustomer_NoPIN_NeedsElevation_NoMutation` → customer erased with no PIN; `TestRestoreBackup_NoPIN_NeedsElevation_NoMutation` → restore staged with no PIN), then restored the fix and confirmed both pass again.
- Repo-wide grep confirmed `EraseCustomer`'s new 4th parameter reached every real caller (one production site, two test sites) with no missed `_test.go`.
- Printf verb parity checked by hand across all four core locales for both new `elevation.summary.*` keys (exactly one `%s` each, matching the single `fmt.Sprintf` argument at each call site) — the exact bug class ut-docs#1865/#1873 exist to catch.
- PII discipline verified directly: the erase elevation summary and its audit payload never carry name/phone/email; the JS posts only `id`.
- Full gate: `gofmt -l .` (clean), `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test ./...` (all green, including the full `internal/data`/`internal/pages` suites), every CI-blocking guard in `ci.yml`'s `build` job, `make docs-shots` regenerated and `guard-docs-shots.sh` green.

## Safe-to-merge verdict

**Yes.** All independent-review findings addressed (2 nits + 1 binding-rule fix, all fixed; 1 accepted as a deferred follow-up, 1 judged not a real gap). Full gate green after fixes.

## Explicitly deferred

- The `restore_staged` audit-row-lives-in-the-replaced-DB property (finding 4 above) — a pre-existing characteristic of this handler, not new here.
- `ut-plugin-language-{de,es}` key updates — same-cycle follow-up, landed *before* merging this PR (see PR description) per the reviewer's own sequencing note: `lang-pack-drift` is blocking on push to `main`, and merging core first would turn `main` red for the interval it takes the pack PRs to land.
