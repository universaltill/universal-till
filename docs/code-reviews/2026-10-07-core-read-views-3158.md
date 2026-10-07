# Review: core read views + `view_query` (ADR-0121 build 6, ut-docs#3158)

**Card:** ut-docs#3158, build card 6 of ADR-0121's task breakdown. A WASM
plugin can now read core data, but only through named, versioned,
read-only views. The first five are today's "Ask your till" tools, so the
AI engine can move out of core (#2851) and keep its tool loop.

## What shipped

- `internal/data/core_views.go`: the view registry, an in-code map that
  ADR §5 calls the "core table".
  - The views are `sales.by_day.v1`, `items.top.v1` and
    `payments.breakdown.v1` (`view:sales`), `stock.levels.v1`
    (`view:inventory`) and `audit.summary.v1` (`view:audit`). Each wraps
    the existing `POSRepo` method its Ask tool calls, with the same bounds:
    `days` 1–365 (default 14), `limit` 1–50 (default 10), and the audit
    summary's 100-row cap.
  - `CoreView.ParseArgs` is strict. An unknown key, a non-integer, an
    exponent or an out-of-range value is an error; a missing argument takes
    its default.
  - `RunCoreView` applies the 5 s deadline. It returns `[]` (never `null`)
    and refuses a result over the byte cap with `ErrCoreViewTooLarge`. It
    never truncates.
  - `ParseBusinessDayStart` moved here. `pages.parseBusinessDayStart`
    delegates to it, so the view and `/reports` share one parser.
- `internal/plugins/wasm_views.go`: the `view_query` host function (buffer
  ABI), registered in the `ut` module. It checks, in order:
  1. at most 64 calls per event → `-5`;
  2. unknown view → `-1`;
  3. view not in the manifest's `views_used` → `-2`, with a
     `permission_denied` audit row. The manifest is read once per event; no
     readable manifest fails closed;
  4. `view:<class>` grant through `CheckPermission` → `-2`, audited, and
     revocable live (checked every call);
  5. bad arguments → `-4`;
  6. query error or deadline → `-3`;
  7. result over 256 KiB → `-5`.
- Tests:
  - `internal/data`: registry, argument edge cases, empty result and byte
    cap, and pinned JSON row keys (`TestCoreViewRowShapesArePinned`). A
    field added to `LowStockItem` for a page can't silently change
    `stock.levels.v1`.
  - `internal/plugins`: driven through a real wasip1 guest
    (`testdata/view_guest`): result equals `RunCoreView`; every refusal
    code; audit rows; live revocation; no manifest; buffer ABI prefix and
    full length; over-cap `-5`; `[]`; the per-event call cap and its reset
    on the next event.
  - `internal/pages` `TestCoreViewsMatchAskTools` (the card's AC): each
    view's JSON equals its Ask tool's on one seeded DB, with several
    argument sets. The DB has a 04:30 business-day start and a sale at
    02:00 yesterday, so a view that ignored the setting fails at any time
    of day.
- Docs: README and `docs/plugin_guidelines.md`. In ut-docs:
  `reference/contracts/plugin-views.md` (new, 1.0.0),
  `reference/plugin-host-functions.md`, `architecture/wasm-runtime.md`.

## Review

Independent review by Fable 5.1 in its own worktree. Verdict: safe to
merge, no blockers.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| S1 | should-fix | `viewListed` re-read the manifest (SQL + file + parse) on every call | **Fixed:** resolved once per event in `hostState` (`viewsOnce`). `views_used` only changes on reinstall, which reloads the module; the grant stays per call |
| S2 | should-fix | No call quota, although ADR §3 says every call counts against the plugin's quota. A tight loop over `stock.levels.v1` keeps the till busy for the whole event deadline | **Fixed:** `viewCallsPerEvent` = 64 → `-5`, tested, documented in the contract |
| N1 | nit | ADR-0121 §2/§3 write `view:<name>`; §5 and the code use `view:<class>` | Noted in the contract doc. The ADR text is left as is: §5 is the normative list, and build 3 already shipped the class form |
| N2 | nit | `strictInt` accepted exponent forms (`1e2`) | **Fixed:** refused, with tests |
| N3 | nit | `lastDays` / `businessDayStartRe` duplicate `pages.daysArgWindow` / `eodTimeRe` | Accepted. The parity test pins them together, and the Ask tools leave core with #2851 |
| N4 | nit | No per-view row-cap field in the registry | Accepted for v1. Add it with a paged `.v2` |
| note | — | `audit.summary.v1` gives a plugin till user ids (`actor_id`) | As ADR §5 requires (parity with the Ask tool). Documented in the contract |

## Verified beyond the automated tests

- The reviewer ran 7 mutations, and each failed its test as expected:
  1. drop the `view_query` export;
  2. skip the `views_used` check;
  3. ignore the business-day setting;
  4. tolerate an unknown argument;
  5. remove the byte cap;
  6. skip `CheckPermission`;
  7. return `null` for an empty result.
- The author re-ran mutation 3 against the parity test and saw it fail.
- Probed argument parsing for duplicate JSON keys. The last value wins
  and is the one validated, so there is no bypass. `+7`, `0x10`, `"7"` and
  `2147483648` are all refused.
- `go build ./...`, `go vet`, `gofmt -l` (clean) and `go test ./...`
  (exit 0 before the review fixes; `internal/plugins`, `data` and `pages`
  re-run after them). Every guard in ci.yml's `build` job passes, except
  two this container can't run: `guard-shellcheck-version` (no shellcheck
  binary) and `guard-deadcode-baseline` (the deadcode build refuses this
  module's go1.27). CI runs both. The one test-only exported helper was
  removed so deadcode has nothing new to flag.

## Not done here (non-goals)

- Core's own Ask tools still call the repo directly. #2851 moves them into
  the AI plugin, onto these views.
- Refusing, at install, a `views_used` name that core doesn't know.
- Customer/PII views: ADR §5 requires their own permission and ADR.
