# Review: bundled lock refresh (ut-docs#6565)

Date: 2026-11-23. Branch `fix/6565-bundled-lock-refresh`. Part of the payments-finish batch; the combined record is
`docs/code-reviews/2026-11-23-payments-finish-1123.md` (branch `fix/6653-tender-journal`).

Pre-flight: review record: this file · help / i18n / UI: n/a (lock + tests) · docs-shots: guard passes (test files only) · ADR-0194 unchanged · follow-ups: none.

Checked: stripe 1.9.0 and sumup 1.9.2 bundles downloaded from GitHub releases into an empty directory, SHA-256 equals `bundle_sha256`; all three plugins are the latest releases; `TestCommittedLock_IsNotBehindTheFloor` stops the lock lagging again; tests follow the lock version; SumUp 1.9.x fails closed with no key (test updated to the real behaviour, not weakened). `go build`, `go test ./internal/plugins/bundled` and the pages plugin tests pass.

Findings: none. Verdict: approve. Deferred: none.
