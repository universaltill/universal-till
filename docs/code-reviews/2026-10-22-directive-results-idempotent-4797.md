# Code review — cloud directives applied once per directive id (ut-docs#4797)

Date: 2026-10-22. Lane `lane:cloud-41`. Built by Sonnet, reviewed by an
independent Opus subagent (one round), TDD re-verified by the orchestrator.

## What shipped

- When the till's result post failed, the cloud kept the directive pending
  and re-delivered it, and `Tick` applied it again. The old comment said
  "the hooks are idempotent", which was false for `adjust_stock` (adjust,
  receive with cost, count) and for `print_report` and similar hooks. A
  retry booked a second stock movement.
- New per-till table `directive_results` (migration 088, checksum pinned,
  listed as not-synced) and `data.DirectiveResultsRepo` (Save first-wins,
  Get, MarkPosted, Prune).
- `cloudsync.handleGenericDirective` serves every directive on the generic
  `apply` path:
  - A stored result for the id is re-posted with no apply.
  - Otherwise the directive is applied, its result stored, then posted.
  - The row is marked posted on 200, or on 404 (the cloud no longer holds
    the directive).
  - A Get error leaves the directive pending; it is never applied blind.
- Pattern: guest-order results (078).
- `guestResultStatusIsFinal` is renamed `resultStatusIsFinal`, with
  unchanged behaviour.
- Docs (ut-docs): `reference/manage-shop-catalog-api.md` §3 and §3.6, plus
  an amendment note in ADR-0128 (no decision changed).

## Findings (independent review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A Prune error made Save return an error after the row was inserted, so the caller logged a false "re-delivery would re-apply it". | **Fixed**: Prune is best-effort inside Save (logged). |
| 2 | minor | A final 4xx other than 404 (such as 403) marked the row posted while the cloud still held the directive pending. After the 7-day posted prune, a re-delivery would have applied it again. | **Fixed**: only 200/404 mark posted, so the row is kept 30 days. New tests `TestTick_RefusedResultPostKeepsRowUnposted` (red before the fix, green after) and `TestTick_NotFoundResultPostMarksRowPosted`. |
| 3 | nit | A transient hook failure is stored as `failed` and re-posted, not retried. | Accepted and commented: the outcome is the same as a successful first post, and the owner re-saves under a new id. |
| 4 | nit | A persistent Get error stalls generic directives, with only a Warn. | Accepted: failing closed is correct, and the table is created by migration. |
| 5 | nit | ADR-0128 still said "`Tick` repeats after a lost result post". | **Fixed** with an amendment note. |
| 6 | nit | Doc lines in the ut-docs edit were badly wrapped. | **Fixed**. |

Checks against ut-cloud:
- Directive ids are `uuid.New` and never set back to pending, so no type is
  wrongly deduped.
- A duplicate result post answers 404.
- `created_at` and the prune cutoffs are both RFC3339 UTC strings, so the
  string comparison orders correctly.

## Verified beyond automated tests

- TDD re-verified in a separate worktree: with `cloudsync.go` at
  `origin/main`, all 6 new Tick tests fail (each hook ran 2 times, want 1).
  With the fix they all pass.
- The 403 test fails against the pre-review code and passes after fix 2.
- Gate: gofmt clean, `go build ./...`, `go vet`, `go test ./...`,
  golangci-lint 0 issues, `guard-data-access.sh` and the other `ci.yml`
  guards.
- Two guards could not run in this sandbox and are left to CI:
  - `guard-deadcode-baseline`: the deadcode binary was built with go1.26
    and this module needs go1.27.
  - `guard-shellcheck-version`: no shellcheck installed. No shell script
    changed.
- No UI surface was touched, so no screenshots.

## Residual (documented in code)

A crash between a hook's commit and the Save can still re-apply the
directive once.

## Deferred

The card's low-priority extra asks the admin directive list to show a
receipt apart from an adjustment. That belongs to #4795's ut-cloud/my.
work, and no code here touches it.

**Verdict: safe to merge.**
