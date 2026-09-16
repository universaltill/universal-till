# Code review: `internal/pages` deadcode-baseline slice (ut-docs#1566)

**Date:** 2026-09-16
**Branch:** `fix/1566-deadcode-internal-pages-slice`
**Card:** universaltill/ut-docs#1566 ("Burn down 97 unreachable functions in
universal-till"), this cycle's per-package slice: `internal/pages` (9 of the
71 remaining `scripts/ci/deadcode-baseline.txt` entries —
`common/deps.go` x1, `eod_method_tax_bands.go` x2, `eod_tax_bands.go` x2,
`kitchen_print.go` x1, `refund_page.go` x1, `setup_language_catalog.go` x1,
`setup_tax_catalog.go` x1). Fourth slice after `internal/httpx`
(universal-till#1148), `internal/data` (universal-till#1149) and
`internal/plugins` (universal-till#1153).
**Complexity:** hard (card-level; this slice specifically touches EOD
tax-reporting code, so triaged with the same care as `internal/pos`'s tax
functions per the card's own body).
**Review model:** Opus (independent subagent, fresh context, isolated
worktree), per `MODEL-ROUTING.md`'s hard-tier routing. Dev ran on Fable per
the same routing.

## What this slice does

Each of the 9 baseline entries was individually investigated (repo-wide
caller search across production code and tests) before deciding its
disposition.

**Deleted (1), verified to have zero remaining callers and no lost
capability:**

- `blockingPaymentEvent` (refund_page.go) — a thin `_, err :=
  blockingPaymentEventWithResponse(...); return err` wrapper. Both blocking
  legs of the payment-provider contract (refund gate, tender authorize gate)
  already call the response-returning forms directly
  (`blockingPaymentEventWithResponse` / `...WithResponseAndID`), so the
  wrapper had zero production callers — only its own 4 test call sites in
  `payment_event_test.go`. Deleted test-first (TDD order): removed the
  function, confirmed `go vet` fails with `undefined: blockingPaymentEvent`,
  then migrated the 4 call sites to call `blockingPaymentEventWithResponse`
  directly, discarding the response — same pattern as the `internal/plugins`
  slice's `tcpConnRegistry.Get`→`GetWithAddr` test migration. No test
  deleted; baseline line removed (71→70).

**Kept with a doc comment explaining the no-caller state (8):**

- `Deps.WaitForAsyncWork` (common/deps.go) — named explicitly in ut-docs#1566's
  own issue body as a legitimate test helper by design. Every caller is a
  `pages`-package test (`t.Cleanup` before DB/TempDir teardown, or a test
  joining a background goroutine before asserting on its side effects).
  Production shutdown does **not** go through it — `app.Run` drains the same
  `sync.WaitGroup` via `drainBackgroundServices(&deps.AsyncWork, …)`
  (ut-docs#513), a *timeout-bounded* wait; `WaitForAsyncWork` is deliberately
  the unbounded form so a leaked goroutine hangs a test loudly instead of
  timing out silently. **Found and fixed a stale pre-existing comment**
  (deps.go, both near `AsyncWork`'s own doc and near `WaitForAsyncWork`
  itself) that claimed production shutdown *does* call this method —
  verified against `internal/app/app.go` that it doesn't, and corrected both
  spots (review caught the fix only being applied to one of the two).
- `computeEODTaxBands` / `attachEODTaxBands` (eod_tax_bands.go) and
  `computeEODMethodTaxBands` / `attachEODMethodTaxBands`
  (eod_method_tax_bands.go) — standalone single-breakdown forms, each reading
  sales independently via `repo.SalesForTaxBands`. Production never calls
  these directly today: `generateEOD` (eod_api.go) reads
  `SalesForTaxBandsInstant` itself and calls the `*FromSales` aggregation
  functions directly (ADR-0066 Decision 6, commit `8548a3af`), and the
  range-export handler calls `attachEODBands`, which does the same
  single-shared-read trick (ut-docs#1004 — avoids two independent sales
  reads racing a concurrent sale on `/api/reports/eod/range`). Only this
  package's own tests (`eod_tax_bands_test.go`, `eod_method_tax_bands_test.go`,
  `tax_summary_test.go`) call the standalone forms, for cases that only need
  one breakdown and don't care about read-consistency between the two.
  **Found and fixed two stale pre-existing comments** here too: one in
  `eod_api.go`-adjacent doc comments claiming `generateEOD` calls
  `attachEODBands` (it doesn't, since `8548a3af`), and test-file helper
  comments (`etbEndOfDay`, `emtbEndOfDay`) claiming to run "the REAL
  production pair/trio ... exactly as generateEOD does" (also no longer
  true after the same commit). Independent review caught a further
  inaccuracy in the dev pass's own fix — a parenthetical that conflated
  ut-docs#1004 (introduced `attachEODBands`) with `8548a3af` (later moved
  `generateEOD` off it) as a single change; corrected to attribute each
  step to the right commit.
- `buildKitchenTicket` (kitchen_print.go) — its own doc comment already
  called it "legacy"; production printing goes through `printKitchen` →
  `buildKitchenTargets` (station routing, ut-docs#516/#2098). Kept
  deliberately rather than migrated: it is the **independent oracle** for
  `TestPrintKitchen_ZeroStations_ByteIdenticalLegacyTicket`, which
  byte-compares `printKitchen`'s real output against
  `RenderKitchenTicket(buildKitchenTicket(...))` — deriving the expected
  bytes from `buildKitchenTargets`' own default bucket instead would compare
  `printKitchen` against itself and prove nothing. Both paths share
  `kitchenTicketFor`/`kitchenItemsFor`, so production ticket-assembly logic
  is still exercised either way; the ~13 test call sites additionally get
  modifier/order-type/charset assertions without station-routing fixtures.
  Independent review caught the doc comment's original test-file list
  wrongly including `print_api_test.go` (only a prose mention there, no
  actual call) — corrected to the two files that really call it.
- `resetSetupLanguageCatalog` / `resetSetupTaxCatalog` (setup_*catalog.go)
  — no change needed; both already carry accurate "tests only (the cache is
  package-global …)" doc comments from before this slice.

No behaviour change anywhere: every non-test edit in this slice is
comment-only except the removal of the dead `blockingPaymentEvent` wrapper.

## Independent review

Opus, fresh context, isolated worktree (`isolation: "worktree"`), per
`complexity:hard` routing (Dev on Fable, Review deliberately not Fable).
Verified every claim above against the real code independently rather than
trusting the dev pass's summary — re-derived each "zero production callers"
claim via repo-wide grep, read `internal/app/app.go` and `eod_api.go`
directly rather than trusting the doc-comment claims about them, confirmed
`TestPrintKitchen_ZeroStations_ByteIdenticalLegacyTicket` really does what
its citing comment says, and **mutation-tested** the migrated
`blockingPaymentEvent` test: swallowing the decline branch in
`blockingPaymentEventWithResponseAndID` makes `TestBlockingPaymentEventGate`
fail at the expected assertion — not a false pass. Ran the real `deadcode`
tool rootless (no GTK/WebKit headers in this sandbox) and confirmed zero new
unreachable functions anywhere in the module, with `internal/pages`
reporting exactly the 8 kept baseline lines.

Found no blocking findings. Found 5 non-blocking comment-accuracy nits — one
half-fixed stale claim in `deps.go`, two "exactly as generateEOD does" test
helper comments left stale by an earlier commit, one wrong test-file
attribution, one commit-attribution mixup in the dev pass's own fix — all
fixed in this branch after review (see file-by-file notes above). Found one
legitimate but out-of-scope pre-existing issue: `refund_page.go`'s
`ListPaymentEntries` error path fails open (identical to "no entry
configured"), same as its mirror at `pos_api.go:571` — filed as
universaltill/ut-docs#2278 rather than fixed here, since changing it is a
real behaviour change to a payment-blocking gate and this slice's
acceptance criteria forbids behaviour change.

## Verified beyond automated tests

- Read `internal/app/app.go`'s actual shutdown path (`drainBackgroundServices`
  call site and `asyncWorkDrainTimeout`'s value) rather than trusting the
  pre-existing doc comment about it.
- Read `eod_api.go`'s `generateEOD` and the range-export handler directly to
  confirm which sales-read path each one actually takes today, rather than
  trusting either the pre-existing comments or the dev pass's first-draft
  correction of them.
- Repo-wide grep for every symbol touched, confirming no caller outside
  `_test.go` files for each kept/deleted entry.
- Mutation test on the payment gate (described above).
- Full gate re-run after the review's own comment fixes: `gofmt -l .`
  (clean), `go build ./...`, `go vet ./internal/pages/...`, `go test
  ./internal/pages/...` (all packages `ok`), `golangci-lint run
  ./internal/pages/...` (0 issues), `guard-data-access.sh`, `guard-i18n.sh`
  (both pass). `guard-deadcode-baseline.sh` itself needs GTK/WebKit headers
  not present in this sandbox — substituted with a direct rootless
  `deadcode` run, same as the prior three slices; real CI has the headers
  and is the actual gate.

## Baseline

`scripts/ci/deadcode-baseline.txt`: 71 → 70 lines. Diff is exactly one
removed line: `internal/pages/refund_page.go: unreachable func:
blockingPaymentEvent`. The 8 kept entries stay on the baseline (that's how
"kept with a doc comment" resolves per this card's own acceptance
criteria — the file tracks *currently unreachable*, not *unresolved*).

~70 baseline entries remain across `internal/pos` (16, tax code — the card's
own body says to triage carefully), `internal/plugins/marketplace` (4),
`internal/manual` (4), misc single-entry files, and others. Next slice
should pick one of those per-package, per the card's "split into several
small PRs by package" acceptance criterion.
