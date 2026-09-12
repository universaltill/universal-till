# internal/httpx deadcode baseline slice — universaltill/ut-docs#1566

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#1566 ("Burn down 97 unreachable functions in
universal-till"), scoped to a single package (`internal/httpx`) per that
card's own "split into several small PRs by package, not one large one"
acceptance criterion.
**Complexity:** `complexity:hard` (the umbrella card's label) — Dev via
Fable subagent, independent review via Opus subagent in an isolated
worktree.

## Background

`ut-docs#1581` (the deadcode CI-gate baseline card) merged 2026-09-06,
seeding `scripts/ci/deadcode-baseline.txt` with 82 current entries (down
from the original 97-function measurement — some had already been fixed
incidentally by #1581's own PR). #1566 depends on that gate existing so the
burn-down can't silently regrow; confirmed merged before picking this card
up this cycle.

Of the baseline's 82 entries, 7 were in `internal/httpx`:

| function | disposition |
|---|---|
| `JSON[In, Out]` (httpx.go) | **deleted** — zero production callers |
| `NewMux()` (httpx.go) | **deleted** — zero production callers |
| `NewRenderer` (httpx.go) | left as-is — already carries an explanatory comment from a prior review (ut-docs#1320) |
| `Renderer.Render` | left as-is — same reason, same function family |
| `IconNames` (icons.go) | left as-is — already documented "for tests and tooling" |
| `ResetCacheForTests` (tplcache.go) | left as-is — the card's own body explicitly names this as a legitimate test helper that stays |
| `FormatQty` (currency.go) | left as-is, **new doc comment added**, follow-up filed |

Also removed 2 unrelated already-burned-down baseline entries found via a
live `deadcode` run before starting (something now calls them that didn't
when the baseline was seeded): `SigningDeviceCredentialStore.Exists`,
`Manager.InstalledIDs`.

## What shipped

- Deleted `JSON[In, Out any]` (a generic `http.HandlerFunc` builder) and its
  dedicated test `TestJSONHandlerSuccessAndError` together — the test's own
  subject is the deleted function, not shared scaffolding for anything else.
- Deleted `NewMux() *http.ServeMux` (trivial one-line wrapper around
  `http.NewServeMux()`) and its dedicated test
  `TestNewMuxDispatchesRegisteredRoutes` together, same reasoning.
- `FormatQty` (locale digit-shape quantity formatting, parallel to
  `FormatMoney`'s existing on-screen substitution) has zero production
  callers — every real call site uses `FormatQtyLatin` instead, which is
  correct for ESC/POS printing but doesn't explain why on-screen quantity
  display doesn't get the same digit-shape treatment on-screen amounts
  already get. Rather than wire this up inside a mechanical cleanup PR,
  filed **universaltill/ut-docs#2221** to scope it properly (BA/UX pass:
  is this a real i18n gap or deliberate?) and added a doc comment on
  `FormatQty` pointing at that card. `FormatQty` is unchanged otherwise.
- Refreshed `scripts/ci/deadcode-baseline.txt`: 82 → 78 lines.

## What the independent review found

Opus, in an isolated worktree (Dev ran on Fable per `complexity:hard`
routing, so review deliberately used a different model). Verdict: **safe
to merge, zero blocking findings.**

Verified independently (not just re-reading the diff):
- **Zero production callers for both deleted functions** — repo-wide grep
  for `JSON(`/`httpx.JSON`/`NewMux(` outside the deleted test code, plus
  the decisive check: `go vet ./...` type-checks every `_test.go` file in
  every package, and passed clean on the reconstructed post-deletion tree
  — there is no reference anywhere in the module.
- **`FormatQty` genuinely has no non-test caller** (word-boundary grep
  excluding `FormatQtyLatin`), confirmed it isn't reachable via any
  template `FuncMap` string-key registration either (deadcode analysis
  blind spot the reviewer specifically checked for), and confirmed
  `ut-docs#2221` exists, is open, and its body matches the new comment's
  framing.
- **Baseline hygiene**: 82→78, exactly the 4 claimed lines removed, file
  still sorted/deduplicated (`sort -c`, `sort | uniq -d` both clean).
- **Inverse check**: restored both deleted tests against the post-deletion
  code in a throwaway copy — `go vet` fails with `undefined: NewMux`,
  confirming the tests were genuine consumers correctly co-deleted, not
  orphaned scaffolding. Import bookkeeping (dropped `errors`, kept `json`/
  `strings`/`net/http`) verified correct.
- Full gate on the reconstructed post-deletion tree: `gofmt -l` empty
  (touched files and whole-repo sanity pass), `go build ./...` clean,
  `go vet ./...` clean, `go test ./internal/httpx/... -v` — 146 passed, 0
  failed, `golangci-lint run ./internal/httpx/...` — 0 issues,
  `scripts/ci/guard-deadcode-baseline.sh` — clean with **no** burned-down
  section printed at all, meaning current output now matches the baseline
  file exactly (no stale leftovers). The same guard on the pre-deletion
  tree independently reproduced the 2 unrelated burned-down entries this
  PR also removes.
- Orchestrator additionally ran `go test ./internal/pages/...` (no
  `-race` — see the disclosed, separate ut-docs#2191/#2219 for that
  package's known `-race` timeout) on the actual feature branch as a
  broader blast-radius check: all green (`internal/pages` 290s,
  `catalog`/`common`/`itemsnav`/`settingsnav` all pass).
- Secret/client-name scan over the diff: no hits (expected — the diff is
  2 deletions + a doc comment).

Non-blocking notes from the review, addressed here:
- WIP commit message replaced with a real conventional one (this commit).
- Missing code-review record — this file.
- The reviewer flagged the new `FormatQty` comment's framing as slightly
  narrower than reality (one of `FormatQtyLatin`'s 3 callers,
  `self_order_shop.go`, feeds a kiosk counter order line that is both
  printed AND shown as a staff-facing on-screen summary per that data
  type's own doc comment) — the reviewer's own assessment is that this
  *strengthens* rather than contradicts the comment's point (on-screen
  quantity display is Latin-only today, tracked as #2221), so left as
  written rather than reworded.

## What was verified beyond automated tests

- Manual repo-wide search confirming no route-registration file, `main.go`,
  or `cmd/` entry point references either deleted function.
- The `deadcode-baseline.txt` diff was reviewed line-by-line against the
  live `deadcode` tool's own before/after output, not just trusted from
  the commit.

## Safe-to-merge verdict

**Yes.** No behaviour change to any shipped code path; two genuinely dead
functions removed along with their own dedicated tests; one genuinely
unreferenced function left in place with an honest comment and a real
tracked follow-up rather than silently deleted or silently wired up.

## Explicitly deferred

- `internal/httpx`'s `NewRenderer`/`Renderer.Render`/`IconNames`/
  `ResetCacheForTests` baseline entries: no action needed, already
  correctly resolved (documented dead code / declared test helpers) before
  this card picked them up.
- `FormatQty` wiring decision: universaltill/ut-docs#2221 (new).
- The remaining ~75 baseline entries outside `internal/httpx` (plugin
  marketplace, `internal/pos`, `internal/pages`, `internal/data`, misc
  small utilities): #1566 stays open, tracking the rest of the burn-down
  in future per-package slices, per that card's own "split into several
  small PRs" instruction.
