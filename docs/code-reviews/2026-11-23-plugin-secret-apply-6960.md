# Review record: set_plugin_secret apply + key code on the till (ut-docs#6960, ADR-0213)

- Scope: internal/pluginsecretapply, directivekey.CurrentKID, cloudsync hook/redeem client, pages secret-code row, en keys, help.
- Reviewer: combined Tester + Reviewer (Sonnet 5.5, not the Dev). Fake keys only; owner tills untouched.
- Pre-flight: gofmt, build, vet, golangci-lint 0 issues, guards (data-access, i18n, i18n-meta, core-neutral, rtl, mail, card-data, compliance, fixed-dates, no-showmodal, no-native-confirm) green, full `go test ./...`. Playwright e2e and a real camera/OSK run were NOT done (no e2e deps in the worktree; no device run by order).

## Findings
1. IMPORTANT, fixed: `TestSettingScope_EveryUsedKeyIsClassified` failed: the setting-key scanner reads any string const named `*Key`, so `ReasonNoTillKey = "no_till_key"` looked like an unclassified settings key. Renamed to `ReasonNoKeyPair` (value and wire contract unchanged). `internal/data` green.
2. Note: `internal/pages` and `internal/plugins` hit the 10-minute package timeout only when run concurrently with other heavy suites; both green when run alone.
3. Verified: HPKE info/AAD pinned by the shared vector (byte-identical to ut-docs#6974); allow-list compile-time; manifest `type: secret` required; kid checked; `ver` must equal the payload ver and exceed the stored version (rollback refused); error text is a bare reason code; value never logged; redeem is main-till only (409 on satellite), code cleared after submit; ADR-0082 guard unchanged and total, one named import allowance test. Trial merge with open PRs #2699, #2694, #2693 is conflict-free.
- Verdict: green after the fix above.
