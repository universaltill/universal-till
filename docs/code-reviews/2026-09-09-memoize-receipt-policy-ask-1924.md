# 2026-09-09 — Memoize receipt.policy.ask per bus generation (ut-docs#1924)

## What shipped

`askReceiptPolicy(ctx, db)` in `internal/pages/receipt_policy_hook.go` was a
plain, unmemoized free function asking installed plugins (via the event
bus) which receipt policies a store's market permits. It sits behind
`printerConfigChecked`, which has 12 call sites including the
checkout/tender handler — a hot path. The sibling `charge.policy.ask` hook
already solved this exact problem via `pluginChargePolicyAsker`
(`charge_hook.go`): memoize per `plugins.SharedBus(db).Generation()`, behind
a mutex, re-asking only when the generation moves.

- `internal/pages/receipt_policy_hook.go`: the free function becomes a
  method, `(*pluginReceiptPolicyAsker).AskReceiptPolicy(ctx)`, on a new
  struct (`db`, `mu sync.Mutex`, `gen uint64`, `known bool`, `cached
  receiptPolicyAnswer`) — the same shape as `pluginChargePolicyAsker`,
  field-for-field and comment-for-comment where the reasoning transfers.
  Since `receipt.policy.ask` has no `pos.Service`-level interface to hang
  one long-lived shared instance on (unlike `charge.policy.ask`, injected
  via `SetChargePolicyAsker`), a package-level
  `receiptPolicyAskers map[*sql.DB]*pluginReceiptPolicyAsker` (mutex-
  protected) plus a `receiptPolicyAskerFor(db)` accessor lazily creates and
  caches one asker per `*sql.DB` pointer. Production has exactly one
  long-lived `*sql.DB`, so this is effectively one shared asker; each
  test's own throwaway db gets an isolated entry.
- `internal/pages/print_api.go`: both call sites —
  `printerConfigChecked`'s read path (ADR-0089 Decision 2) and the
  printer-settings POST handler's plugin-permitted check — switch from the
  free function to `receiptPolicyAskerFor(d.Db).AskReceiptPolicy(ctx)`.
- `internal/pages/receipt_policy_test.go`: the four existing
  `TestAskReceiptPolicy_*` tests are updated to construct
  `&pluginReceiptPolicyAsker{db: db}` and call `.AskReceiptPolicy(ctx)`.
  Four new tests mirror `charge_hook_test.go`'s equivalent coverage:
  `TestAskReceiptPolicy_AnswerCachedPerGeneration`,
  `TestAskReceiptPolicy_NoOpinionIsCachedToo`,
  `TestAskReceiptPolicy_ErrorsAndGarbageAreNotCached`,
  `TestAskReceiptPolicy_ReloadInvalidatesCache`.

## Independent review

Fresh-context review with no access to the implementer's reasoning, done
directly in the working checkout (already an isolated worktree for this
task). Actually ran, not just read: `go build ./...` (clean), `go vet
./internal/pages/...` (clean), `gofmt -l .` (no diffs), `golangci-lint run
./internal/pages/...` (0 issues), the targeted test scope
(`-run 'ReceiptPolicy|PostSettingsPrinter.*Receipt' -v`, 20/20 pass), the
same scope again with `-race` (clean), and the full
`go test ./internal/pages/...` suite (green, 3m42s).

**Verdict: safe to merge. No fixes were needed** — the diff is a faithful,
correct mirror of an already-proven pattern.

### TDD re-verification (independent, not taken on trust)

Temporarily changed the cache-hit check from `if a.known && a.gen == gen {`
to `if false && a.known && a.gen == gen {`, disabling the cache read while
leaving the write path intact. Re-ran the two tests that exist specifically
to catch this:

- `TestAskReceiptPolicy_AnswerCachedPerGeneration` — **failed** with a real
  assertion (`asked 3x: plugin ran 3 times, want 1`), not a compile error.
- `TestAskReceiptPolicy_NoOpinionIsCachedToo` — **failed** the same way
  (`declined 3x: plugin ran 3 times, want 1`).

Reverted the mutation; both pass again, and `git diff --stat` confirmed the
working tree matched the original diff exactly (no stray edits left
behind). The new tests genuinely exercise the caching behavior they claim
to.

### Semantic checks (the substance of this review, not just "does it build")

- **Caching a recognized-but-empty answer as no-opinion**: when the bus
  answers (`ok=true`) but nothing survives `validateReceiptPolicies`
  (e.g. `{"allowed_policies":["digital","maybe"]}`), the result is cached
  as `answered: false` for that generation. This is correct: the payload
  (`receiptPolicyAskPayload{}`) carries no per-call inputs, so the same
  installed-plugin state deterministically produces the same validated
  result every time within one generation — there is no plausible reason
  for the same plugin, same generation, to need re-asking sooner. This
  mirrors `pluginChargePolicyAsker`'s "both a real answer and a clean
  no-opinion cost the same module boot, so both are cacheable" reasoning,
  and the reasoning genuinely applies here — the only difference from
  charge policy is that receipt policy's validation step *can* reduce an
  "answered" response down to empty (`validateChargePolicy` never does,
  it always returns a policy), and the diff correctly folds that case into
  the same cacheable no-opinion bucket rather than inventing a third state.
- **Not caching a JSON-unmarshal failure**: treated as always-uncached,
  same as a transient `bus.Ask` error, while a clean/recognized-empty
  decline is cached. Checked whether this actually transfers from
  `charge_hook.go` or is copied without re-checking fit: it transfers. A
  malformed answer is a plugin bug, not a stable declaration of policy —
  it isn't safe to assume the *next* malformed answer will be identical
  the way a validated empty list is guaranteed to be (it's static input,
  not proof of determinism), and declining-without-caching costs nothing
  extra in the case that matters (no plugin installed at all, the
  overwhelmingly common case today) because `HasSubscribers` still short-
  circuits before any of this runs. The only case that repeatedly pays the
  full WASM-ask cost is a plugin that is installed AND persistently
  returns garbage — identical to `pluginChargePolicyAsker`'s own accepted
  trade-off, not a new regression.
- **Mutex usage**: locked only around the cache read and the cache write;
  the blocking `bus.Ask` call happens with the lock released, exactly
  matching `pluginChargePolicyAsker`'s locking shape line-for-line
  (confirmed by diffing the two functions side by side). `-race` run
  confirms no detected data race on the new code path.
- **Package-level `receiptPolicyAskers` map / no eviction**: entries
  accumulate for the life of the process and are never removed. In
  production this is a non-issue (one long-lived `*sql.DB`, one entry,
  forever). Checked whether it's a real risk in a long test binary:
  `internal/pages`'s test files call `openPagesTestDB(t)` 131 times, but
  only tests that actually exercise `print_api.go`'s HTTP handlers (not
  the ones constructing `&pluginReceiptPolicyAsker{db: db}` directly, which
  is most of `receipt_policy_test.go`) ever touch
  `receiptPolicyAskerFor` and grow the map — a few dozen at most across
  the whole package's test run. Each retained `*sql.DB` was already closed
  via the test's own `defer db.Close()`, and `sql.DB.Close()` releases the
  underlying OS connections/file descriptors itself regardless of whether
  the Go struct is later garbage-collected — so this cannot manifest as
  "too many open files." What's retained is a handful of small, already-
  inert struct pointers for the lifetime of one test binary process, which
  exits at the end of the run anyway. Not a practical risk; noted as an
  accepted, already-documented trade-off (the diff's own comment explains
  why there's no `pos.Service`-level home for a single shared instance),
  not something to fix now.
- **Both `print_api.go` call sites**: both switched identically
  (`receiptPolicyAskerFor(d.Db).AskReceiptPolicy(ctx)`), preserving each
  site's original `ctx` (the checked-read path's caller `ctx`, the POST
  handler's `r.Context()`) — no behavior change beyond the memoization.
- **Other ADR-0089 pillars untouched**: Decision 1 (legacy
  `printer.auto_print` → `receipt_policy` derivation) and Decision 3
  (Germany carve-out via `receiptPolicyLockedForCountry`, applied last)
  are structurally unchanged in `print_api.go` — only the single line
  implementing Decision 2 changed, and only to route through the new
  asker with identical return semantics (`ok=false` still always means
  "unrestricted," never an error surface).
- **Recurring bug classes**: no file-write handler in this diff (nothing
  writes to disk), so `os.MkdirAll` doesn't apply; no new cwd-relative
  path literal anywhere in the diff, so `paths.Data(...)` doesn't apply.
  Confirmed by grep, not just by inspection.
- **No real client/shop names, no secret-shaped literals**: grepped the
  full diff for client-name and credential-shaped patterns — none found.
  Test plugin IDs are the existing placeholder style
  (`com.universaltill.tax-xx`).

## Verified beyond automated tests

- `go build ./...` — whole repo, clean.
- `go vet ./internal/pages/...` — clean.
- `gofmt -l .` — no files need formatting.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- `go test ./internal/pages/... -run 'ReceiptPolicy|PostSettingsPrinter.*Receipt' -v` — 20/20 pass.
- Same scope with `-race` — clean, no data races detected.
- `go test ./internal/pages/...` (full package suite) — green, 220s.
- Manual mutation test (see TDD re-verification above) — confirmed the new
  cache tests are load-bearing, not vacuous.

## Deferred / follow-up

None. This is a self-contained, mechanical mirror of an already-reviewed
pattern (`pluginChargePolicyAsker`) with no new design surface, no UI
surface, and no open questions left over from the checks above.

## Safe-to-merge verdict

**Yes.** Build, vet, gofmt, lint, and the full targeted + full-package test
scopes are all clean; the memoization semantics are correct and the
precedent-copied reasoning genuinely holds for this event's shape; locking
is correct and race-detector-clean; the two other ADR-0089 pillars in this
file are unaffected; no client-name/secret leakage; neither recurring bug
class (`os.MkdirAll`, `paths.Data`) applies to this diff.
