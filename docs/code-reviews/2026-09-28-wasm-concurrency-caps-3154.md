# Review: WASM call caps, mobile process-plugin refusal, /plugin/* headers (ut-docs#3154)

- **Date:** 2026-09-28
- **Card:** ut-docs#3154 (ADR-0121 build card 2, the remainder after #2891). Split out of #2851.
- **Author:** Opus 5.5 (lane:cloud-24). **Reviewer:** Fable, independent subagent, one round.

## What shipped
- `internal/plugins/wasm_concurrency.go`: `wasmCallGate` caps concurrent WASM
  calls at 4 per plugin and 16 in all (Pi/desktop), or 2 and 8 (Android/iOS),
  per ADR-0121 §2. One slot per plugin and one globally are reserved for
  sale-path events: any `.ask`/`.authorize`/`.refund` and `fiscal.sign.*`,
  except the long export/import transfers. `HandleEvent` acquires a slot
  inside the call's own deadline and releases it with `defer`. The release
  is idempotent.
- `internal/plugins/supervisor.go`: `StartPlugin` refuses process plugins
  (`runtime` "go"/"native") on android/ios.
- `internal/pages/plugin_page.go`: the `/plugin/*` CSP gains
  `frame-ancestors 'self'`, and responses get `X-Content-Type-Options: nosniff`.
  The rest of ADR §7's policy (`default-src`/`script-src 'self'`) waits for
  #2913, because these pages render inside the POS chrome with its inline
  scripts.
- Already on `main` from #2891: the memory limit, the egress/SSRF checks and
  the own-port refusal. Nothing was redone.
- Docs: ut-docs `architecture/wasm-runtime.md` → "Sandbox limits".

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | The sale-path list missed `charge.policy.ask` (checkout totals) and `receipt.policy.ask`. ADR §2 says "any `.ask` / `.authorize` point and `fiscal.sign.*`". On a tablet, a running export would have stalled every basket recompute by 2 s, and the result fell back to the fail-closed charge policy. | **Fixed:** the classification now follows the ADR (every `.ask` except export/import), and the tests cover both policy asks. |
| 2 | minor | On mobile, queued non-blocking events (`sale.completed`) wait 2 s and are then dropped while the plugin's single ordinary slot runs an export or import. | **Accepted and documented** in wasm-runtime.md. Follow-up: ut-docs#3171. |
| 3 | minor | `supervisorGOOS` was restored without `t.Cleanup`. | **Fixed.** |
| 4 | nit | Calling `acquire` on a nil gate panicked. | **Fixed:** a nil gate means no limit, and a test covers it. |
| 5 | nit | Future reentrancy: when `event_publish` lands, a synchronous same-plugin chain needs as many slots as its depth. | **Comment added** on the gate. |

## Verification
- TDD: `TestWasmHandleEventSalePathGetsReservedSlot` was checked against a
  mutated build in which every call counted as sale-path. The test failed
  ("ordinary event on a saturated plugin ran"), then passed once the change
  was restored. The CSP test failed before the header change ("X-Content-Type-Options = \"\"").
- `gofmt -l .` produced no output. `go build ./...`, `go vet`, and `go test ./...` passed (full run before the
  review fixes). `go test ./internal/plugins ./internal/pages` passed after the fixes, and so did
  `golangci-lint run ./internal/plugins/... ./internal/pages/...` (0 issues).
  The reviewer's `go test ./internal/plugins -race` passed.
- Guards: every `ci.yml` guard passed locally except two failures that exist on `main` too:
  `guard-deadcode-baseline.sh` (no GTK headers locally, so the desktop root is skipped) and
  `guard-shellcheck-version.sh` (no shellcheck binary in this container).
- No UI copy, no SQL, no file writes, no locale keys.

## Verdict
Safe to merge once finding 1 is fixed. It is fixed.
