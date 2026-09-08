# ut-docs#1723 — Free table surfaces a cross-till risk signal it can honestly give

Date: 2026-09-08
Branch: `fix/1723-free-table-cross-till-signal`
Review: one independent subagent, fresh context, Opus (`complexity:medium`),
isolated worktree.

## The gap

`POST /api/tables/{id}/release` (the manager "Free table" escape hatch)
gave zero signal about cross-till risk: if a table's live claim belonged
to a *different* till, the manager had no way to know whether that till
might have a real held order parked there — `held_sales` is never synced
cross-till (deliberately; `internal/data/sync_admin_repo.go`'s own
comment). Split from ut-docs#1704 (which made `table_claims` itself
cross-till aware but explicitly left `held_sales` out of scope).

**Design finding that reshaped the card's own draft acceptance
criteria:** the card's "cheapest" option — have the primary ask each
enrolled, online till whether it holds a held order for this table — is
not actually cheap. This codebase has **no primary→replica reverse-RPC
mechanism anywhere**: every cross-till call here is replica-initiated
(write-through/pull), and `tills` stores no replica network address.
Building that would be a materially larger architectural change than
this card's own scope, and duplicates the "much bigger change" its
non-goals already rule out for `held_sales` replication. So true
cross-till held-order *detection* is not built here — that remains a
real, separate, larger card if ever prioritized.

## The fix

Surface the strongest **honest** signal the primary already has locally,
without any new network call: which till owned the released claim
(`table_claims.till_id`) and whether that till was seen recently
(`tills.last_seen_at` within the existing `tillClaimTTL` = 2 minutes —
the same bound `ClaimTableForTill` already uses for claim staleness).

1. `internal/data/tables_repo.go`: `ForceReleaseTableClaim` now takes a
   `recentCutoff time.Time` and returns a `TableReleaseResult` struct
   (`Released`, `StillHeld`, `OtherTillRecentlySeen`, `OtherTillID`)
   instead of `(bool, bool, error)`. It reads the claim's `till_id`
   **before** the `DELETE`, in the same transaction, then — only when
   that till_id is non-empty (i.e. not this till's own local
   `till_id=""` claim) — checks whether `tills.last_seen_at` for that
   till falls within `recentCutoff`.
2. `internal/pages/tables_page.go`: the handler passes
   `time.Now().Add(-tillClaimTTL)` and, when `OtherTillRecentlySeen` is
   true and `StillHeld` is false (a genuine local held order still wins
   — definite evidence beats a hint), redirects to the new
   `tables.error.released_other_till_recent` message instead of the
   plain success redirect. The audit log gains
   `other_till_recently_seen` / `other_till_id`.
3. New i18n key added to all four locales this repo ships (`en`, `ar`,
   `fa`, `tr`) — a static string, since `httpx.T()` has no placeholder
   substitution, so till identity stays audit-log-only, never in the UI.
4. One sentence added to `web/help/en/tables.md`, qualifying the
   existing "Free table can't warn you about an order it doesn't know
   exists" caveat rather than contradicting it.
5. Regression tests: `internal/data/tables_repo_test.go`'s
   `TestForceReleaseTableClaim_OtherTillRecentlySeen` (recently-seen /
   stale / unknown-till / own-local-claim), and
   `internal/pages/tables_page_test.go`'s
   `TestTablesPage_ReleaseWarnsOnRecentlySeenOtherTill` (the new
   redirect, the audit-log fields, and the held-order-wins precedence
   case).

**Explicit non-goal, recorded rather than silently dropped:** genuine
cross-till detection of a held order itself needs a primary→replica
reverse-RPC mechanism this codebase doesn't have — a separate, larger
architectural card if ever wanted.

## Independent review (Opus, isolated worktree)

**Verdict: SAFE TO MERGE**, after one blocker was fixed.

- **Blocker found and fixed in this same branch, before merge:**
  `scripts/ci/guard-docs-shots.sh` failed — this diff touches a
  screenshotted route's Go file (`internal/pages/tables_page.go`) and a
  manual topic (`web/help/en/tables.md`), both of which move the
  guard's tracked "surface" hash. The reviewer proved this was
  introduced by the diff (the same guard passes clean at the diff's
  base commit) rather than pre-existing drift. Fixed by running
  `make docs-shots` and committing the regenerated
  `web/help/img/manifest.json` plus the screenshots it touched
  (`en/tables.png` is byte-identical — the default page view doesn't
  show the new flash message — three unrelated screenshots picked up
  incidental non-determinism from a Chromium point-version mismatch
  already called out as non-fatal by `docs-shots.sh` itself, ut-docs#622).
- **One low-severity fix applied:** `TableReleaseResult.OtherTillID`'s
  doc comment claimed `""` means "no claim, or this till's own local
  claim" — incomplete, since a **stale/no-longer-enrolled foreign till**
  also yields `""` (the field is only set inside the
  `OtherTillRecentlySeen` branch). Comment-only, no behavior change;
  fixed to say the field is only meaningful paired with
  `OtherTillRecentlySeen`.
- **TDD independently re-verified**, not taken on trust: reverted
  `tables_repo.go` + `tables_page.go` to the pre-fix commit, confirmed
  `internal/data` fails to **compile** (exact signature-mismatch errors
  the new tests produce) and `internal/pages` compiles but the new
  handler test genuinely **fails at runtime** (redirects to the old
  plain `/tables` instead of the new warning key) — a real
  red-before-green, not just a compile artifact. Restored and confirmed
  `git diff HEAD` empty (byte-identical) and all tests green again.
- **Correctness/security, specifically checked:**
  - Transaction ordering: the `till_id` read genuinely precedes the
    `DELETE` inside one transaction; `internal/db` sets
    `_txlock=immediate` (`BEGIN IMMEDIATE`), so there is no
    read-then-upgrade TOCTOU window at all.
  - Traced all three paths into `till_id=""`'s handling (no claim,
    local claim via `ClaimTable`, migration 008's `NOT NULL DEFAULT
    ''`) and confirmed a manager's own till can never be reported as
    "another till."
  - No SQL injection (fully parameterized); no new file I/O (neither
    recurring `MkdirAll`/`paths.Data` bug class applies — no file
    writes in this diff).
  - No sensitive leakage: the UI string is static and generic; till
    identity is audit-log-only (an internal UUID, not a secret).
  - The message's wording was checked against the actual guarantee the
    code provides (a hint, not proof) and matches.
  - No real client/shop name or secret-shaped literal anywhere in the
    diff.
- **UX/manual check:** the new key reuses the page's single existing
  `.login-error` element (`web/ui/pages/tables.html`) — zero new
  markup/CSS, same pattern every sibling message on this page already
  uses. The `ar`/`fa`/`tr` translations are non-empty, distinct, and
  contain no stray literal directional wording.

## Deferred, owed this same cycle (not part of this PR)

`web/locales/en.json` gained a new key. Per this ecosystem's lane-
ownership rule, the lane merging this core change owns landing the same
key in the external `ut-plugin-language-de` and `ut-plugin-language-es`
packs before picking up anything else — `lang-pack-drift` is advisory on
this PR but **blocking on push to `main`**.

## What was verified beyond automated tests

Drove the real compiled binary (not just `httptest`) end to end: built
`unitill-pos`, ran it against a fresh SQLite file with `UT_AUTH=off`,
created a real table over HTTP, seeded a till + recently-seen
`last_seen_at` + a foreign `table_claims` row directly in the live DB,
and confirmed the real `POST /api/tables/{id}/release` call redirects to
the new key and that `GET /tables?err=...` renders the exact expected
sentence through the real template pipeline. Confirmed the regression
case too: an unrecognized/untracked till's claim still redirects to the
plain `/tables` success path, unchanged from before this fix.
