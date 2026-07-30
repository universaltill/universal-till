# Code review — coverage batch 7: internal/data (auth_repo + stragglers)

- **Date:** 2026-07-30
- **Branch/PR:** `coverage-batch7-internal-data`
- **Author:** pipeline (fable)
- **Independent reviewer:** opus subagent (different model, adversarial brief)
- **Scope:** tests-only — `internal/data/auth_repo_test.go` (new),
  `internal/data/batch7_stragglers_test.go` (new). No production code changed.

## What shipped

Batch 7 of the coverage push (`ut-docs/QUEUE.md` "Test-coverage push,
remainder"). Package `internal/data`: **58.5% → 63.3%**. Scope was honestly
split — `internal/data` is 12k lines with 246 functions under 100%, so this
batch took the security-relevant zero-coverage surface plus the small
stragglers; `pos_repo.go` (42 uncovered funcs) and `plugin_repo.go` (25) are
queued as batches 8–9.

- **`auth_repo.go` (0% → ~85%)** — first-ever coverage of the operator-auth
  surface: user CRUD (unique usernames, COALESCE'd NULL pin_hash), login
  candidates (active + non-empty PIN only, both NULL and `''` excluded),
  last-admin-lockout guard (`CountOtherActiveAdminsWithPIN` matrix), and the
  full session lifecycle — expired, revoked, and deactivated-user sessions
  all proven to deny; purge keeps live + grace-window rows and deletes
  revoked + long-expired; user-wide revoke leaves other users untouched.
- **Stragglers** — `InvoiceRepo.BySale`/`ByDisplayNo` (kind-filtered),
  `Create` series numbering (per-series sequential, replica prefix embeds,
  `UNIQUE(sale_id,kind)` duplicate guard verified against the real index),
  `InstallStatusRepo.Get`, `SettingsRepo.All` (+ wrap/wrapf error branches
  via closed-DB probes), `ModifierRepo.ItemIDsWithModifiers` (active-only,
  empty-input no-SQL path), `POSRepo.InsertSaleLineModifiers` (in-tx,
  NULL-vs-`""` id columns, empty-mods no-op).

All covered functions verified live (every one has real production callers —
no dead-surface coverage theater, per the batch-4 honesty rule).

## Verification

- **9 mutation probes total, all failing correctly against broken code**:
  4 by the pipeline pre-review (LookupSession revoked filter,
  CountOtherActiveAdmins exclusion, ItemIDsWithModifiers active filter,
  PurgeDeadSessions cutoff — the first two attempts at #2/#4 turned out to be
  semantically equivalent mutations and were redone with real ones), 5 fresh
  ones by the independent reviewer (expiry rejection, deactivated-user
  filter, nullableString NULL persistence, empty-pin exclusion, and
  TouchSession's revoked guard — see finding 1).
- Reviewer cross-checked caller contracts: `internal/auth/service.go`'s
  `VerifyPIN(pin, u.PinHash)` needs the hash populated (asserted), idle-lock
  reads `LastSeenAt` (parse layouts asserted); kitchen-ticket read path
  unaffected by the modifier-id nullability.
- Full gate: `go build ./...`, `go vet`, full-repo `go test` (zero failures),
  `internal/data` under `-race`, guard-data-access + guard-i18n — all green,
  re-run after the review fixes.
- Tests-only diff → no runtime surface to drive; no e2e needed.

## Independent review findings (all addressed)

1. **SHOULD-FIX (real false-pass, fixed):** the TouchSession revoked-no-op
   assertion could never fire — TouchSession writes 1-second-resolution
   RFC3339 and the test runs in milliseconds, so before/after strings
   collided identically even with the guard deleted. Fixed with a sentinel
   `last_seen_at` (a real write can never equal it); the previously-surviving
   mutation (drop `AND revoked_at IS NULL`) now fails the test — re-verified
   personally, restored, suite green.
2. **Misleading comment (fixed):** the modifier test claimed empty-string ids
   "would violate the FK" — `sale_line_modifiers.group_id`/`option_id` have
   no FK (migration 017); comment corrected to the real rationale (NULL keeps
   "no source group" queryable) and now states the empty-id case is
   defensive, not a live write path (reviewer's nit 3, same comment).
3. **Nit (heeded):** three unrelated untracked `docs/*.md` planning files in
   the tree (they have their own QUEUE.md item) — commit adds files
   explicitly so they can't be swept in.

Real-name check clean ("Task Runner" + generics only); no secrets; no
wall-clock flakiness (minute/hour margins only); per-test temp DBs, no
ordering deps.

## Accepted remainder (documented, not covered)

The batch files' residual <100% is error branches on a healthy DB
(QueryContext/Exec/Scan failures, rows.Err) — reachable only by fault
injection disproportionate to their risk; `repoObservability.wrap`'s
nil-logger branch is unreachable in practice (logging always initialised).
`invoice_repo.Create`'s retry loop is covered for first-attempt success and
permanent-duplicate break; a transient-then-success retry would need a
racing writer harness — deferred.

## Verdict

**SAFE TO MERGE** (reviewer's explicit verdict; both its should-fixes
resolved and re-verified before commit).
