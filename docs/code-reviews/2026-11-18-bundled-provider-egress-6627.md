# Review — bundled payment plugins reach the provider host only while net: is granted (ut-docs#6627)

Date: 2026-11-18. Branch `test/6627-bundled-provider-egress`. Lane `lane:cloud-41`.
Built by Sonnet (2 dev rounds), tested and reviewed by Opus 5.5 (different model), one review round.
Precedent: universal-till#2587 (`2026-11-18-ios-bundled-payment-plugins-review2-3433-6565.md`) shipped
acceptance criterion 1 (`bundled.Sync` grants declared permissions); this closes criterion 2.

## What shipped (test-only, no production change)
- `internal/plugins/bundled_provider_egress_test.go` (`//go:build bundledplugins`, `package plugins_test`):
  `TestBundledProviders_AuthorizeReachesProviderHostOnlyWhileNetGranted`, subtests `stripe`, `sumup`.
  The REAL embedded wasm is installed by `bundled.Sync` on the App Store channel, keys set as the
  shop sets them (secrets sealed), plugin enabled and loaded. A local TLS fake provider (cert for the
  provider host, verified against a test root — no InsecureSkipVerify) receives the authorize call;
  only DNS (host → a public IP) and the final dial are stubbed, so the production `net:<host>` gate
  and `egressDialer` checks run. Asserts method/path, `Authorization: Bearer <configured key>`,
  outcome `approved`. Then `net:<host>` is revoked: zero new requests, the plugin is still loaded and
  declines (`exit_code(2)`), and the log shows `plugin egress denied: host <host>`.
- `internal/plugins/export_provider_egress_test.go`: test-only hook `SetTestEgressForTest` wiring the
  existing `egressEnv` stub into `WasmRuntime.httpClient`.
- `.github/workflows/ci.yml` `bundled-plugins` job: runs the new test (`-run`, ~5 s).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | should-fix | revoked step passed on any failure (unloaded/crashed plugin) | fixed: asserts still-loaded decline + egress-denied log (+ stripe `network_error`) |
| R2/T1 | should-fix | SumUp fake answered every path alike; plugin polled ~23 s to `outcome_unknown` | fixed: fake routes checkout + transaction lookup; sumup approves in ~2.5 s |
| R3 | nit | cleanup reset data dir to fallback | fixed: previous dir restored |
| R4 | nit | part (a) ignored the result | fixed: `approved` asserted for both |
| T2 | low | Authorization only checked for a `Bearer ` prefix | fixed: exact configured key on every request |
| T3 | low | `err != nil` weak for SumUp revoke | covered by R1 |

## Verification beyond unit tests
- Tester mutations (all red in the expected direction, all reverted): revoke→grant in the test;
  production gate always-allow (`admitHTTPHop` exact=true); always-deny; `egressDialer.check` always
  deny. Dev mutation: `GrantDeclaredPermissions` removed from `bundled.go` → red.
- Orchestrator re-ran personally after the fix round: test green (5.4 s); revoke→grant mutation red
  for both subtests, reverted.
- gofmt, `go vet` (tagged and untagged), `golangci-lint` 0 issues (tagged and untagged), untagged
  `go build ./...` and `go test ./internal/plugins/bundled/`. 73 guards from `ci.yml`: all pass except
  `guard-armv7-seccomp` (no qemu-arm), `guard-shellcheck-version` (no shellcheck), `guard-catimport-version`
  (no catimport tags fetched), `guard-deadcode-baseline` (go1.27 needed) — each fails identically on clean
  `main` in this container; CI runs them.

Pre-flight: n/a — test + CI only; no help topic, screenshots, strings, reference doc or README affected
(the help text "enter your keys … press Enable" is now accurate since #2587). No follow-ups.
