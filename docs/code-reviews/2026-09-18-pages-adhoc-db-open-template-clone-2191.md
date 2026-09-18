# internal/pages: clone the migrated template DB in 6 more ad-hoc `db.Open` sites

**Card:** universaltill/ut-docs#2191 (not closed by this PR — see Scope below)
**PR:** universal-till#(this branch) `fix/2191-pages-adhoc-db-open-template-clone`
**Model routing:** `complexity:hard` — Dev via Fable subagent, independent review via Opus subagent (isolated worktree).

## What shipped

`internal/pages`'s test suite has historically paid a full from-scratch
SQLite migration chain on every `db.Open(...)` call, which is what made
`go test ./internal/pages/... -race` time out (ut-docs#2191). Two
already-merged PRs fixed the two shared helpers with the biggest call-site
counts (`newRealDBDeps` — universal-till#1145 — and `openPagesTestDB` —
universal-till#1198) by building one fully-migrated SQLite template file
once per test binary process and cloning its bytes per test instead of
re-running migration.

This PR converts the next-heaviest remaining **ad-hoc** `db.Open(...)`
call sites — ones with no shared helper, each hand-rolling its own
`db.Open(filepath.Join(t.TempDir(), "name.db"))` — to call the existing
`openPagesTestDB(t)` helper instead:

| file | sites converted |
|---|---|
| `kitchen_print_test.go` | 10 |
| `init_test.go` | 9 |
| `held_order_claim_reaffirm_test.go` | 5 |
| `self_order_counter_mode_test.go` | 3 |
| `print_api_test.go` | 3 |
| `inventory_prediction_test.go` | 3 |
| **total** | **33** |

Test-only change. No production code (`internal/db` included) touched.
No new helper introduced — this only adds callers of the existing one.

## Scope decision

~74 ad-hoc sites remained across ~46 files per ut-docs#2219's own count.
This PR takes the 6 heaviest files (33 sites) — the same "heaviest lever"
files #2219's own review already named as the next target — rather than
all 46, to keep the diff reviewable in one pass (per this pipeline's "what
one item is allowed to cost" guidance). The remaining ~40 files (1-2 sites
each) are filed as a follow-up Backlog card
(universaltill/ut-docs#2385), mirroring the exact #2191→#2219 chaining
pattern already used twice on this issue. **ut-docs#2191 stays open** —
closing it needs the full package to actually complete under `-race`,
which needs the follow-up card too.

## Independent review (Opus, isolated worktree)

Verdict: **safe to merge as-is, no blocking findings.** Full findings
below; commands were actually run, not just read.

- **Correctness of all 33 conversions**, verified by reading each site:
  `internal/db.DB` is `type DB struct { *sql.DB }` with only unexported
  methods, so `&db.DB{DB: openPagesTestDB(t)}` is behaviourally identical
  to what `db.Open` returned. No accidental DB-handle sharing (two-DB
  sites — primary/replica in `held_order_claim_reaffirm_test.go` and
  `init_test.go` — still get two independent clones). No site relied on
  the dropped literal filename. No site needed fresh/un-migrated/
  specifically-sequenced migration — `init_test.go`'s multi-boot tests
  still hold one `*db.DB` across both `Init()` calls against the same
  on-disk file; only how that file got its schema changed.
- **`print_api_test.go`'s `TestAsyncPrintGoroutinesFinishBeforeWaitForAsyncWorkReturns`**
  (the one non-mechanical conversion — it used to hand-manage its own
  `os.MkdirTemp`/`os.RemoveAll` per loop iteration): verified empirically
  that `t.TempDir()` still hands back a distinct directory per call within
  one test function (3 loop iterations → 3 independent dirs/DBs, confirmed
  with a throwaway repro), so the per-iteration isolation the original
  manual cleanup provided is unchanged; the dropped filesystem-symptom
  assertion was already flagged in the test's own doc comment as the
  false-pass version of this test.
- **No unused imports / dead code** — `go vet`/`golangci-lint` are both
  clean (either would hard-fail on an unused import in a test file).
- **No behavior change from the shared helper's PRAGMAs/pool settings** —
  `journal_mode=MEMORY`/`synchronous=OFF` only drop crash-durability,
  which nothing in these 6 files asserts on; `SetMaxOpenConns(1)` was
  checked specifically against the two files serving `httptest` handlers
  off the same pool the test body also queries (sequential flows, no
  deadlock across repeated `-race` runs, and it's the same shape the
  already-merged ~144 `openPagesTestDB` sites already use).
- **The two recurring bug classes this pipeline watches for** (missing
  `os.MkdirAll` on a file-write handler; a cwd-relative path where
  `paths.Data(...)` belongs) — not applicable: this diff adds no file
  writes and no new path construction (it only removes path construction).
- No real client/shop name, no secret-shaped literal introduced.

Non-blocking notes (no action taken): a mid-loop `t.Fatalf` in
`print_api_test.go`'s async test would leave that iteration's DB open
when `t.TempDir()`'s cleanup runs — harmless on Linux (no Windows runner
in this repo's CI) and no worse than the old code's own swallowed
`_ = os.RemoveAll(dir)`.

## What was verified beyond automated tests

- `gofmt -l internal/pages/` — clean.
- `go build ./...` — clean.
- `go vet ./internal/pages/...` — clean.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- `bash scripts/ci/guard-data-access.sh` / `guard-i18n.sh` /
  `guard-kiosk-engine.sh` — all pass (irrelevant surfaces untouched, run
  anyway as the standard pre-commit gate).
- `go test ./internal/pages/... -count=1` — green, run independently
  twice (once by Tester, once by the reviewer): **184.965s / 187.406s**,
  matching the pre-change baseline shape (no test newly failing; `go test
  -list` byte-identical to `main` aside from timing — 2663 tests either
  side, nothing deleted/renamed).
- `-race` timing on the touched-tests subset, compared directly against
  parent commit `0fa9712`: **138.851s on `main` vs 28.759s on this
  branch (~4.8x)** for the same test set. `-count=3` and a targeted
  `-race -count=2` on the concurrency-shaped tests
  (`TestInit_*`/`TestHeldOrderClaimReaffirmTick_*`) both green, no
  flake, no `WARNING: DATA RACE`.
- **`go test ./internal/pages/... -race -count=1` for the full package
  still does not complete within budget** — expected, and not a
  regression: the remaining ~40 ad-hoc sites (follow-up card) and any
  aggregate overhead across the package's full ~2663-test suite still
  dominate. This PR's own measured contribution (the 4.8x figure above)
  is the real, isolated signal for what it changed.

## Safe-to-merge verdict

Yes. Independent Opus review found no blocking issues; Tester's gate
(build/vet/lint/guards/tests) is green; git identity re-verified before
commit.

## Explicitly deferred

- The remaining ~40 files / ~40 ad-hoc `db.Open` sites in `internal/pages`
  — filed as universaltill/ut-docs#2385.
- Whether `go test ./internal/pages/... -race` can ever complete
  end-to-end within a normal CI timeout, vs. needing a `-timeout` bump or
  test sharding for this package specifically — ut-docs#2219 already
  recorded this as an open question; unchanged by this PR.
