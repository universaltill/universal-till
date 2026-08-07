# Inventory never reached a replica — `/api/sync/stock` missing from the auth middleware's exempt list (ut-docs#388)

## What shipped

`internal/auth/middleware.go`'s `exempt()` allow-lists the machine-to-machine
sync surface — those endpoints are authed by a **per-till bearer inside the
handler**, not by an operator session. `/api/sync/stock` was missing from
it, so the session middleware rejected the replica's stock-sync pull with
401 *before* `syncTill`'s bearer check ever ran. The replica authenticated
perfectly and was refused anyway.

Confirmed on real hardware, not inferred: a two-till shop where till 2
showed no inventory, indefinitely. The bearer was provably valid (hashes to
the primary's `tills.bearer_hash`, `last_seen_at` current) and
`/api/sync/admin` — which *is* exempt — pulled fine on the same loop, in the
same seconds, while `/api/sync/stock` 401'd every 30s with no operator-
visible signal. After adding the path: inventory row counts matched across
both tills (62 = 62).

Fix: add `"/api/sync/stock"` to the `exempt()` switch case, plus
`TestSyncPullPathsAreExempt`, which independently states which paths the
replica's pull/push loop needs exempt and asserts operator surfaces
(`/api/sync/promote`, `/api/sync/pair`, `/api/sync/tills/…`, `/settings`,
`/api/inventory/low-stock`) stay **non**-exempt so the allow-list can't
quietly become a hole.

## Merge conflict against `main`

Between this fix's branch point and merge, `main` independently added three
more `/api/setup/*` wizard routes (LAN discovery/pairing, ut-docs#289) to
the *same* `exempt()` switch statement — a real, expected conflict, not a
duplicate/competing fix. Resolved by keeping both path sets (11 total) and
merging the two comment blocks into one coherent narrative. Verified this
repo's own log history shows no other independent fix landed for this bug
before merging (`git log main -- internal/auth/middleware.go` only shows
the unrelated setup-wizard addition).

## Independent review (fresh-context Sonnet subagent, `complexity:easy`)

Per this pipeline's model-routing rule, an easy card reviews at a
fresh-context instance of the same model that wrote it rather than a
different model — the independence comes from never having seen the dev
reasoning, not a different tier.

**Verdict: safe to merge, no findings.** What the reviewer actually did,
not just read:

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/auth/...` (full package, including
  `TestSyncPullPathsAreExempt`) — clean.
- **TDD claim re-verified independently**: removed `/api/sync/stock` from
  the `exempt()` case in the actual working tree, re-ran the test, got the
  exact expected failure (`middleware_test.go:34: /api/sync/stock is not
  exempt — this middleware will 401 it before the handler's bearer check
  runs, so replicas can never use it`), restored the fix via
  `git checkout --`, confirmed green again and the working tree clean. Not
  vacuous.
- **Verified the merge resolution itself**, not just the original fix:
  diffed `bdb6925` → `0c61f2d` and confirmed all 11 exempt paths survived
  (both this fix's `/api/sync/stock` and `main`'s three `/api/setup/*`
  additions), no duplicates, nothing silently dropped, comments read as one
  narrative rather than a mangled splice.
- **Checked the newly-exempted path actually has its own auth**, so this
  isn't opening an unauthenticated hole: `GET /api/sync/stock`
  (`internal/pages/sync_admin.go:69`) calls `syncTill(r, tills)` and 401s
  immediately on failure — the same per-till-bearer-hash lookup
  (`internal/pages/sync_api.go:163-173`) as its already-exempt siblings
  `/api/sync/ping`/`/api/sync/snapshot`/`/api/sync/sales`/`/api/sync/admin`.
  `exempt()` only removes the *session-cookie* gate; the handler's bearer
  check is untouched.
- Checked the two recurring bug classes this pipeline watches for (missing
  `os.MkdirAll`, cwd-relative path instead of `paths.Data(...)`) — not
  applicable, this diff has no I/O at all.
- Checked for real secrets/shop names in the diff — none; test data is
  literal API path strings only.
- Checked whether this needs a `web/help/` manual update — no: this is an
  internal auth-middleware fix with zero shop-owner-visible surface, and it
  makes already-documented sync behavior actually work rather than changing
  what's documented.

## Safe to merge

Yes. Feature branch `fix/stock-sync-401`, merged via `merge` (not
squash/rebase, per this pipeline's standing merge-method rule) after `main`
was merged into it to resolve the exempt-list conflict.
