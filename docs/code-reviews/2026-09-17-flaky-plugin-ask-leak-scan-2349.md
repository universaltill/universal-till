# 2026-09-17 — flaky leak scan in `TestTaxHookEmitsPluginAsk` (ut-docs#2349)

**Card:** ut-docs#2349 · **Lane:** local · **Complexity:** easy (fresh-context Sonnet review, Opus fixes)

## What broke

`main` CI `build` failed on the #2223 merge commit (`7aeece67`, run 35159867226):
`TestTaxHookEmitsPluginAsk` scans the marshalled `plugin_ask` ring event for
`"4321"`, `"tok_"` and `"700"` — payload/response content that must never
reach the diagnostics stream. The scan covered the whole event, including
`correlation_id`, a random UUID — which that run happened to contain `700`
(`6700505a-…`). Unrelated to #2223; a latent flake on `main`.

## Fix

`leakScanJSON(t, ev, moreVolatile...)` strips the fields that legitimately
carry arbitrary digits (`correlation_id`, `at`, `duration_ms`, `generation`,
plus any caller-named random id) before marshalling for the substring scan.
Every content-bearing field is still scanned, and a field added to the event
later is scanned by default (exclude-list, not include-list).

## Independent review (Sonnet, fresh context)

- **R1 should-fix — accepted.** The sibling scan in
  `TestClaimTableWriteThroughEmitsTableAssignment` checks `"4321"` against an
  event whose `table_id` is `uuid.NewString()` — the same flake class. Fixed by
  passing `"table_id"` as a volatile field for that call.
- **R2** cross-checked `internal/diagnostics/events.go` `PluginAsk` /
  `TableAssignment` wire fields: after stripping, only literal test strings and
  closed-vocabulary enums remain — no random-content field escapes the scan.
- **R3** no weakening: the stripped fields are all internally generated, never
  populated from plugin payload/response content.
- **R4 nit** — doc comment updated to name the `table_id` case.

## Verified

`go vet ./internal/pages` clean; `go test ./internal/pages -run
'TestTaxHook|TestClaimTable|Diagnostics' -count=1` pass; the failed CI job was
re-run on `main` (`gh run rerun --failed`) to unblock the v0.17.0 release while
this PR carries the real fix.
