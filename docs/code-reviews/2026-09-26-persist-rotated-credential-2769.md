# Review: the till keeps its rotated per-device cloud credential (ut-docs#2769, slice 1)

## What shipped

- `enroll.ApplyRotatedCredential`: a `/v1/stores/sync` response's `data.device_id` + `data.device_token` (ADR-0116 D4) is kept **only** when the device id is exactly this till's own, and the token is well formed (16–1024 header-safe characters). It replaces `marketplace.token` and the in-memory copy (`liveToken`), so every cloud caller (sync, check-in, issue reports, diagnostics, alerts, TSE setup, cloudlink) uses it from the next request without a restart. A mismatched or empty id is ignored with one warning; `UT_MARKETPLACE_MERCHANT_TOKEN` pins the token (not persisted, warned once per process); the same token again is a no-op.
- The in-memory copy comes first: a failed save keeps the credential in use and retries on the next sync **and on every check-in tick** (`RetryUnsavedCredential`), because the cloud sends it once.
- Cloud link: `403 device_credential_required` → `WaitReasonCredentialRequired`; the Tills row reads "Paused: needs its own credential" (it used to say "cloud busy", seen on the tablet 2026-09-26).
- The manager's Settings → "All settings" card no longer shows, and `/api/settings/upsert` refuses (403), the credential rows: `marketplace.token`, `marketplace.device_*`, `marketplace.enrolled_at`, `sync.bearer`.
- Keys in en/ar/fa/tr; help `multitill` in en/de/ar/fa/tr.

## Review (Dev: Opus subagent, TDD; independent security review: Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| S1 | should-fix | "All settings" printed the (now non-reissuable) credential in plain text, and its editor could overwrite it. | **Fixed:** `credentialSettingKey` filter + 403. `TestSettings_CredentialsNeverShownNorEditable` failed first (value shown for all 3 keys, upsert 204 and overwritten). |
| S2 | should-fix | A restart before a failed save succeeds loses a one-shot credential. | **Fixed (narrowed):** retry also from the check-in tick. `TestRotatedCredential_FailedSaveIsRetriedFromTheCheckinTick` fails with the retry stubbed out. **Rollout guard, binding:** `TILL_CREDENTIALS_CUTOVER_AT` stays unset until #2816 and till-side pairing (the rest of #2769) have shipped, so a stranded till can recover. |
| S3 | should-fix | The hint and help said "pair the till again", which the till can't do yet. | **Fixed:** "if this stays, contact support" in all 5 help languages and 4 core locales. |
| nit | nit | Text says "next check-in"; strictly the next sync POST (≤ 10 min after a 304). | Accepted: "normally". |
| nit | nit | `tokenExplicit` written without `mu` in `Init`. | Accepted: Init runs before the server; noted. |

The reviewer re-verified two TDD claims by mutation (pushSync ignoring the token → `rotate_credential_test.go:121` fails; dropping the device-id equality → `rotate_test.go:160` and `rotate_credential_test.go:157` fail). It checked the leak paths: logs at every level, error wrapping, the admin bundle, the join snapshot, fleetlink hello and diagnostics. All clean; the Settings card was the one gap, now closed.

## Gate

gofmt; `go build`, `go vet`, desktop-tag build; full `go test ./...`; `-race` on enroll, cloudsync and cloudlink; guards data-access, i18n, help-topics, help-drift; `make docs-shots` (124 passed; only the manifest changed, since no screenshot shows the All-settings card).

**Verdict:** safe to merge. Language packs de/es for the 2 new keys follow in the same cycle.
