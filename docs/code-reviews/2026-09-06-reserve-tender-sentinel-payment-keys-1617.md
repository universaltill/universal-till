# Reserve deriveTenderType's "unknown"/"split" sentinel keys (ut-docs#1617)

## What shipped

`internal/pos/sales.go`'s `deriveTenderType` produces exactly two sentinel
strings: `"unknown"` (zero payments, or a payment with an empty/whitespace
`MethodID`) and `"split"` (2+ distinct method IDs on one sale), after
normalizing every method ID with `strings.ToLower(strings.TrimSpace(...))`.
A future locale-translation lookup for these two literal values (tracked
separately as ut-docs#1579, not yet merged to `main` as of this change)
would rely on the assumption that no payment plugin can ever register
either name as its own payment key. Nothing enforced that assumption:
`internal/plugins/manifest.go`'s `validatePaymentEntryKeys` only checked a
new plugin's payment key against *already-registered* keys, so a plugin
could legally ship a payment method literally keyed `"unknown"` or
`"split"` (or any case variant) today.

Fix: `validatePaymentEntryKeys` now rejects a payment entry whose key,
after the same normalization `deriveTenderType` applies
(`strings.ToLower`), equals `"unknown"` or `"split"` — a new
`reservedTenderSentinelKeys` map, checked alongside the function's
existing empty/whitespace/`:` format checks, before the within-manifest
duplicate-key check and before any DB round trip. This function is shared
by both `PersistManifest` and `Rollback` (`internal/plugins/rollback.go`),
so both call sites are covered without a second edit.

## Independent review

Fresh-context Sonnet subagent (card labelled `complexity:easy`), isolated
worktree (`.claude/worktrees/agent-a56dffb6e4e8879b4`, branched from the
pre-review WIP commit). Verdict: **safe to merge, no blockers.**

Ran independently in its own worktree: `go build ./...`, `go vet ./...`,
`gofmt -l internal/plugins/`, `go test ./internal/plugins/... -run
TestPersistManifest -v` (30/30 pass), full `go test
./internal/plugins/...` (plugins + marketplace + oauth, all green),
`golangci-lint run ./internal/plugins/...` (0 issues), and
`guard-data-access.sh`.

Independently re-verified the TDD claim itself (not just trusted on the
implementer's word): reverted only `internal/plugins/manifest.go` back to
its pre-fix content while keeping the new test, re-ran
`TestPersistManifest_RejectsReservedTenderSentinelKeys`, confirmed it
failed with the expected message, then restored the fix and confirmed it
passed again.

Also checked and confirmed clean: no over-rejection (ad-hoc scratch test
with `unknowns`/`splitting`/`splitx` — substrings/supersets of the
sentinels — correctly accepted, only exact case-insensitive/trimmed
matches are blocked); no Unicode/locale-casing hazard (both sentinels are
pure ASCII, `strings.ToLower` needs no locale-sensitive folding here); no
built-in/seeded tender already uses either key (`internal/db/migrations`),
so this gap could never have been caught by the existing
`FindPaymentKeyConflicts` collision check; no real client/shop name or
secret-shaped literal in the diff; the two recurring bug classes (missing
`os.MkdirAll` on a file-write handler, a cwd-relative path where
`paths.Data(...)` belongs) don't apply — no file or path handling in this
diff. Backend-only change (no UI/template/locale/route touched), so the
UX-guidelines checklist and user-manual-topic check were correctly skipped.

### Findings — both fixed before this commit

1. **Minor (doc accuracy).** The draft comment stated as present fact that
   `internal/httpx.tenderLabel` "translates through the locale table
   (ut-docs#1579)" — that function doesn't exist on `main` yet (`ut-docs#1579`
   is a separate, not-yet-merged card). Reworded to describe the risk
   without asserting the translation helper currently exists.
2. **Minor/nit (message style).** The error named the internal Go function
   `deriveTenderType`, meaningless to a plugin author and inconsistent
   with every sibling error in this function (which describe collisions
   in domain terms, e.g. "already provided by plugin X"). Reworded to
   "reserved for the till's own \"no payment\"/\"mixed payments\"
   tender-type label — pick a different key."

Both fixes re-verified with the full gate before this commit (build, vet,
gofmt, `go test ./internal/plugins/...`, `golangci-lint run
./internal/plugins/...`, `guard-data-access.sh`, `guard-i18n.sh`) — all
green.

## What was verified beyond automated tests

- `go test ./...` (full repo suite) green before the review findings were
  fixed; the package-scoped gate above re-ran green after the fixes.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally: all pass (this diff is Go-only, no i18n/UI/route/manual
  surface touched, so most are no-ops by construction, but all were run
  rather than assumed).

## Safe-to-merge verdict

Safe to merge. No deferred follow-ups — the two findings were fixed
directly rather than deferred, since both were one-line, low-risk edits
to code this same commit already touches.

PR: universaltill/universal-till#(opened alongside this record — see
`Closes universaltill/ut-docs#1617` in the commit/PR).
