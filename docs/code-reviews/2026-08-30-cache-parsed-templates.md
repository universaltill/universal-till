# Cache parsed templates (ut-docs#1320)

**Branch:** `fix/1320-cache-parsed-templates` · **Card:** ut-docs#1320 (`p1`,
`complexity:medium`) · **Source:** finding D of the 2026-08-30 principal-engineer
performance audit (`docs/code-reviews/2026-08-30-performance-audit.md`).

## What shipped

No HTTP handler cached a parsed `*template.Template` — every render call site
(`internal/httpx.Render`/`RenderPartial`/`RenderWith`, `internal/ui`'s
`NewBasketView`/`NewJournalView`/`NewHelpNavView`/`NewRenderer`,
`internal/pages.renderReceipt`) called `template.New(...).Funcs(funcs).ParseFS(...)`
fresh, inside the handler, on every single request. A sale-screen load alone
triggered 4+ full parses, and 2-3 more per subsequent tap during checkout
(nine `ui.NewBasketView` call sites in `pos_api.go` alone).

New `internal/httpx/tplcache.go`:

- `cachedTemplate(key, rootName, funcs, files...)` (unexported) parses a file
  set exactly once per `key`, caching the result in a `sync.Mutex`-guarded
  `map[string]*template.Template` with double-checked locking (parse outside
  the lock, re-check under it before storing).
- `ClonedTemplate(key, rootName, funcs, files...)` (exported, the only
  sanctioned entry point) gets the cached base and returns `base.Clone()`
  with `funcs` bound on the **clone**, never the shared base. Every render
  call site now goes through this.
- `ResetCacheForTests()` (exported, test-only) clears the cache — needed by
  the "works from any working directory" guard tests (see Findings §1).

Design rationale: `html/template`'s contextual auto-escaping inspects a
function's *type signature* at parse time, never its return value or closure
state, so which concrete closures are bound at parse time doesn't affect
correctness — but the closures actually bound at `Execute` time matter a lot
(locale-bound `money`/`T`, and `catalog/handlers.go`'s per-request
`taxCodeName` closure rebuilt from a fresh DB read on every call). So the
cache never executes the shared base directly; every caller clones first and
binds the real, current funcs on the clone.

Six call sites converted: `httpx.Render`, `httpx.RenderPartial`,
`httpx.RenderWith`, `ui.NewBasketView`, `ui.NewJournalView`,
`ui.NewHelpNavView`, `ui.NewRenderer` (buttons.go), `pages.renderReceipt`.
`httpx.NewRenderer` (the standalone one in `httpx.go`) was left unconverted —
confirmed dead code, zero production call sites — with a comment explaining
why and how to wire it up correctly if that ever changes.

## Independent review (Opus, isolated worktree)

Verdict: **safe to merge, no blockers**, four should-fix findings and two
nits, all four should-fixes applied here.

**What the review verified beyond reading the diff**, in its own words:

- Confirmed `Clone()`/`Funcs()` concurrency safety by reading the Go 1.25
  `html/template`/`text/template` source directly (`Clone()` deep-copies
  parse trees into a fresh namespace; `Funcs()` on a clone writes only into
  that clone's own maps under its own lock) — not just trusting the code
  comments' reasoning.
- Wrote a throwaway 2560-call concurrent stress test (64 goroutines × 40
  iterations, each binding a unique closure) under `-race`: zero cross-talk,
  zero races.
- Ran the full gate itself: `gofmt -l .` clean, `go build ./...`,
  `go vet ./...`, `go test ./...` all green, `go test -race
  ./internal/httpx/... ./internal/ui/...` green, `guard-data-access.sh` and
  `guard-i18n.sh` both pass.
- Did genuine broke-then-restored TDD verification on two separate sabotage
  scenarios (see Findings §1 and §3 below) inside its isolated worktree.

### Findings, triage, and fixes

1. **should-fix — fixed.** The process-lifetime cache silently disarmed
   three "works from any working directory" guard tests
   (`internal/ui/render_cwd_test.go`'s three tests,
   `internal/pages/receipt_test.go`'s `TestRenderReceipt_WorksFromAnyWorkingDirectory`)
   — these exist because a previously-shipped bug once made a template
   constructor read from a cwd-relative disk path instead of the embedded FS
   and panicked the till on the first basket add on any install launched
   outside the repo root. Against a warm cache, only the first test in a
   `go test` run to touch a given key still exercises the real parse; every
   later test gets a harmless cache hit and stops testing anything.
   **Fix:** added `httpx.ResetCacheForTests()`, called at the start (and via
   `t.Cleanup`) of `chdirTemp` (`internal/ui/render_cwd_test.go`) and inline
   in the receipt test. **Re-verified myself**, independently of the
   reviewer's own verification: sabotaged `ui.NewBasketView` back to a
   `ParseFiles("web/ui/...")` disk read, ran the FULL `internal/ui` package
   (not the test in isolation) — `TestNewBasketView_WorksFromAnyWorkingDirectory`
   failed, exactly as it should. Restored the fix, full suite green again.

2. **should-fix — fixed.** The original comment and benchmark overstated the
   win: `Clone()` deep-copies parse trees into a fresh, unescaped namespace,
   so html/template's contextual auto-escaping analysis **re-runs on every
   clone** — it is not eliminated, only the lexing/parsing step is. The
   reviewer's own execute-inclusive benchmark measured ~37% faster (not an
   order of magnitude) on the basket render. **Fix:** corrected the
   `tplcache.go` doc comment to state this precisely, and added
   `BenchmarkRenderBasket_Uncached`/`_Cached` (parse-or-cache-hit + clone +
   escape + Execute, the real request shape) alongside the original
   parse-only benchmarks, with a comment pointing at which pair measures
   what. Re-ran: `471217 ns/op` (uncached) → `292871 ns/op` (cached), a 38%
   reduction — consistent with the reviewer's independent measurement.

3. **should-fix — fixed.** `TestClonedTemplate_ConcurrentSafe` had every
   goroutine pass identical funcs, so it had nothing to detect a
   `Funcs()`-before-`Clone()` regression with — such a bug would corrupt
   which locale's/request's closures "win" for concurrent callers of a
   shared key, but passes `go test -race` cleanly (the base's funcmap write
   is itself mutex-guarded, so there's no *data* race, only a *logical* one).
   **Fix:** rewrote the test so each of 64 goroutines binds a uniquely
   identifiable `T` closure and asserts its own output carries only its own
   marker. Verified this actually catches the bug it's named for: temporarily
   made `ClonedTemplate` call `.Funcs()` on the base before `Clone()` — the
   new test failed with `CROSS-TALK` errors naming the colliding goroutines;
   restored, green again. (The reviewer independently confirmed the same
   thing with its own throwaway version of this test, then deleted it as
   agreed — not part of this diff.)

4. **should-fix — fixed.** `CachedTemplate` was exported; since
   `html/template` permanently refuses `Clone()` on a template that has ever
   been `Execute`d, any future caller that executed the cached base directly
   (bypassing `ClonedTemplate`) would poison that key for every other caller,
   for the life of the process — surfacing as a `template.Must` panic on
   every request to that page until restart. **Fix:** unexported it to
   `cachedTemplate`; `ClonedTemplate` is now the only way to get a template
   out of the cache.

5. **nit — addressed via a doc comment.** Documented the invariant that
   every caller sharing a cache key must pass the same *set* of func names
   (values may differ per call; a name present in an earlier call but
   missing from a later one would silently keep the earlier closure, where
   the old per-call `ParseFS` would have failed loudly at parse time).
   Confirmed not reachable today — `FuncsFor` always returns a complete,
   identical key set.

6. **nit — addressed via a doc comment.** `httpx.NewRenderer` (the one in
   `httpx.go`, distinct from `ui.NewRenderer`) is dead code with zero
   production callers (confirmed by grep) and was deliberately left
   unconverted; added a comment explaining why and how to wire it up
   correctly if it's ever used.

### Deferred (backlog-worthy, out of scope for this card)

The reviewer's own suggestion, not pursued here: eliminating the per-request
`Clone()` + escape-analysis re-run entirely by making the *funcs themselves*
stable (read locale/per-request data from the template's data payload or a
context value instead of closing over it), so one parsed-and-escaped template
could serve every request with zero per-call clone. Real further win (~6x
headroom per the reviewer's own numbers: 252µs execute-inclusive → ~43µs
ideal), but a genuine cross-cutting refactor across every call site and
template, not this card's scope. Left as a suggestion for a future card, not
filed as one — the audit report itself already tracks this area and can
route it if judged worth doing.

## Verification beyond the independent review

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./...` — full suite green (includes `internal/data`, `internal/pos`,
  `internal/plugins`, all untouched by this diff but re-run per the
  standing gate).
- `go test -race ./internal/httpx/... ./internal/ui/...` and
  `go test -race ./internal/pages/... -run 'Receipt|Basket|Journal|...'` —
  green.
- All 16 CI-blocking guards from `universal-till/CLAUDE.md`'s "Before
  committing" list — green.
- **Real driven run**, not just Go tests: booted the actual binary via
  `e2e/run-till.sh` (fresh DB, demo catalogue seeded, auth off) and drove a
  real checkout — scanned a demo barcode 32 times (repeatedly, to exercise
  the checkout hot path this fix targets), tendered cash (hits
  `renderReceipt`), and confirmed `/ui/journal` showed the completed sale at
  the correct total (`£38.40` = 32 × `£1.20`). Also confirmed `?lang=fa` then
  `?lang=en` in sequence both rendered correctly (`dir="rtl"` then
  `dir="ltr"`) — the sharpest real-world proof that per-request funcs aren't
  getting stuck to a stale locale under the shared cache. No visible surface
  changed (pure render-layer plumbing, no template/markup edits), so no
  screenshot check was needed or taken.

## Safe-to-merge verdict

**Yes.** No blocking issues found or remaining. All four should-fix findings
from the independent review are applied and independently re-verified
(including my own from-scratch reproduction of finding 1's regression, not
just trusting the reviewer's report). Full gate green under `-race`. No
behavior change to what's rendered — confirmed by the full existing test
suite passing unchanged, by the two locale-correctness tests specifically
targeting the cache's core risk, and by the live driven run above.
