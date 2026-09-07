# price_history sync-classification guard (ut-docs#1671)

## What shipped

`price_history` (`internal/data/sync_admin_repo.go`'s `nonAdminTables`) is
mutated (`AppendPriceHistoryItem`/`Variant` UPDATE the previous open row's
`ends_at`, and item deletion DELETEs rows) and live-consulted at checkout
(`POSRepo.ResolveCurrentPrice` reads an open `price_history` row *before*
falling back to `items`' synced price, so an open row overrides it). That
reason text was already corrected in the merged #1586 PR — this card is
the follow-up "confirm the classification decision" it split off.

Verified before deciding anything: `AppendPriceHistoryItem`/`Variant` have
**zero callers** outside `internal/pos` and its own tests today —
`scripts/ci/deadcode-baseline.txt:67-72` lists the whole
`internal/pos/pricing.go` file (all 6 symbols) as whole-program-
unreachable, cross-checked with a direct grep for both symbol names
repo-wide. Nothing in production can run a price change today, so the
divergence risk the card describes is real but dormant.

**Decision:** leave `price_history` OUT of `adminTables`. Building the
FK-ordering + mutated/hard-deleted-row sync machinery for a feature with
no production entry point is speculative work ahead of need, and mirrors
this same schema-drift-guard PR's own precedent of deferring genuinely-
undecided tables rather than forcing one. To keep that decision from
silently going stale, this PR adds
`scripts/ci/guard-price-history-sync.sh` (+ a companion
`_test.go`-style bash regression harness, `guard-migration-version-
collision.sh`'s `MIGRATIONS_DIR`-override convention, not
`guard-deadcode-baseline.sh`'s whole-program analysis) wired into
`.github/workflows/ci.yml` alongside the other backend guards: it fails
the build if a real caller of `AppendPriceHistoryItem`/`Variant` shows up
anywhere outside `internal/pos/pricing.go` while `price_history` is still
classified `nonAdminTables`, forcing the sync classification to be
revisited at exactly the point the dormant risk goes live.

Deliberately separate from `guard-deadcode-baseline.sh` rather than
special-cased inside it — that guard's "a burned-down baseline entry is
not a failure" behaviour is a deliberate, independently-reviewed generic
design; this is a narrower, price_history-specific assertion layered on
top via plain grep.

No behavior change to sync itself, no Go source touched, no ADR needed
(a CI guard encoding an already-made classification, not a new
cross-cutting mechanism).

## Independent review

Opus, fresh context, isolated worktree (`isolation: "worktree"`, per the
ut-docs#386 mitigation — never shared this session's own checkout).

**Verdict: yes-after-fixes.** Design sound, guard genuinely works;
independently re-verified the premise (zero production callers) rather
than taking it on faith, and re-verified the TDD claim by neutering the
guard's own exclusion filters one at a time and confirming the matching
regression-test cases failed correctly, then restoring.

Commands the reviewer actually ran: `git show HEAD --stat`, the guard
against the clean tree (pass), the full regression harness (all cases
pass, `git status`/`git diff` empty afterward), `gofmt -l .`, `go build
./...`, `go vet ./...`, a YAML parse of `ci.yml` confirming step
placement.

### Findings — all fixed

1. **should-fix — guard failed *open* if the classification file moved.**
   `grep -q ... internal/data/sync_admin_repo.go` inside `if !` swallowed
   grep's exit-2 for a missing file, so a rename/split of that file would
   silently disarm the guard behind a green check. Fixed: an explicit
   `[[ -f ... ]]` existence check that fails loudly first.
2. **should-fix — the `internal/pos` exclusion was package-wide, not
   file-scoped.** The comment said "the package that legitimately wraps
   these functions," but `internal/pos` is 40 files of real production
   code (`catalog_ops.go`, `sales.go`, `inventory.go`) — the most natural
   home for a scheduled-price-change feature, and it would have been
   silently exempt. Fixed: narrowed the exclusion to
   `internal/pos/pricing.go` specifically. New regression case
   (`OtherFileInPos`) proves a caller anywhere else in `internal/pos` is
   now caught — independently mutation-tested by re-broadening the
   exclusion and confirming that exact case fails, then restoring.
3. **should-fix — the original test mutated real tracked source.** The
   first draft edited `internal/data/sync_admin_repo.go` in place via a
   `sed`/backup-file dance to simulate reclassification — demonstrated to
   fail-open on a missing file, leave a stray `.bak` uncleaned on
   SIGKILL, and clobber the file's mode (0600 via `mktemp`) on restore.
   No sibling guard test does this; `guard-migration-version-
   collision_test.sh` explicitly avoids it for the same reason. Fixed:
   the guard now takes a `SYNC_CLASSIFICATION_FILE` env override (mirrors
   `MIGRATIONS_DIR`), and the test points it at scratch files under
   `mktemp -d` for both the reclassification and missing-file cases —
   real tracked source is never touched.
4. **should-fix (doc) — the header overstated coverage.** The guard is a
   direct-caller check, not a call-graph analysis: a one-hop wrapper
   added *inside* `internal/pos/pricing.go` itself that calls
   `AppendPriceHistoryItem` and gets called from `internal/pages` would
   be invisible to it (and to `guard-deadcode-baseline.sh`, whose
   burned-down case is explicitly not a failure either). Fixed: added an
   explicit "known limitation" paragraph to the guard's header so a
   future maintainer doesn't over-trust the green check alone.

Nitpicks not acted on (reviewer's own conclusion, not overridden): the
whole-file `"price_history":` grep for the skip-check is correct in
practice (verified against the real `adminTables`/`nonAdminTables` shape)
and a future false match is a comment-collision edge case not worth
guarding against now; the `_test.go:` line-content filter's line-vs-path
ambiguity is contrived. The empty-array guard on `clear_fixtures` (only
matters on bash <4.4; CI runs 5.2.21) was fixed anyway since it was a
one-line no-cost change.

## Verified beyond automated tests

- The regression harness's own 7 cases, run for real, all pass: two
  real-caller-outside-`internal/pos` rejections (Item + Variant), a
  caller inside `internal/pos` but outside `pricing.go` rejected (the
  fix for finding 2), a `_test.go` caller ignored, a missing
  classification file rejected (the fix for finding 1), a reclassified
  caller ignored via a scratch classification file (the fix for finding
  3), and the clean codebase passing.
- Independently mutation-tested my own fix for finding 2 (not just the
  reviewer's original findings): temporarily re-broadened the exclusion
  back to the whole `internal/pos/` directory, confirmed the new
  `OtherFileInPos` case fails exactly as expected, restored, confirmed
  green again. `git diff --stat` before/after matches (only the two
  guard files changed).
- `gofmt -l .` clean, `go vet ./...` clean, `go build ./...` clean
  (repo-wide, after the fixes too).
- Full `go test ./...` — green. (One transient `internal/pages` failure
  was observed on a run that overlapped with the review agent's own
  concurrent `go build`/`go test` activity in its isolated worktree;
  re-ran `go test -count=1 ./internal/pages/...` in isolation twice and
  both were clean — treated as resource contention between concurrent
  runs, not a regression from this diff, since this diff touches zero Go
  source.)
- `git status --porcelain` clean after every test run — no stray fixture
  files, no mutated tracked source.

## Safe-to-merge

Yes, after the four fixes above (all landed in this same PR, re-verified
against the corrected files with a fresh regression-harness run and
build/vet pass).

## Explicitly deferred (by design, not oversight)

Building the scheduled-price-change feature itself, wiring
`AppendPriceHistoryItem`/`Variant` into any admin page, and modifying
`guard-deadcode-baseline.sh`'s generic mechanism — none of these are in
scope for this card; see "What shipped" above for why.
