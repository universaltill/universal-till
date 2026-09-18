# Code review: misc small-packages dead-code slice (ut-docs#1566)

**Date:** 2026-09-18
**Card:** universaltill/ut-docs#1566 ("Burn down 97 unreachable functions in universal-till")
**Slice:** 12 small, previously-untouched packages (`cmd/unitill-desktop`,
`internal/bluetooth`, `internal/config`, `internal/db`, `internal/diagnostics`,
`internal/discovery`, `internal/fiscal`, `internal/logging`, `internal/money`,
`internal/pihealth`, `internal/print`, `internal/secrets`) — 7th slice, after
`internal/httpx`, `internal/data`, `internal/plugins` (partial), `internal/pages`,
`internal/manual`, `internal/plugins/marketplace`. Deliberately excludes
`internal/pos` (tax code — needs its own careful slice) and the remaining
`internal/plugins` root files.
**Complexity:** hard (per `MODEL-ROUTING.md`) — Dev on Fable (isolated
worktree), Review on Opus (separate isolated worktree, no implementation
reasoning shared).

## Scope: 16 baseline entries

```
cmd/unitill-desktop/control.go: controlServer.LastInputAt
internal/bluetooth/android_bridge.go: SetAndroidBridge
internal/config/i18n.go: NewI18n
internal/db/db.go: BaselineStatementsFor
internal/diagnostics/events.go: EnumValues
internal/discovery/sweep.go: SweepPrinters
internal/fiscal/tse_credential_store.go: NewSigningDeviceCredentialStoreAt
internal/fiscal/tse_credential_store.go: SigningDeviceCredentialStore.Path
internal/logging/logging.go: ResetRecent
internal/money/money.go: Sum
internal/pihealth/pihealth.go: CheckNow
internal/print/escpos.go: Doc.Validate
internal/print/kitchen.go: RenderKitchenTicketText
internal/secrets/keystore.go: KeyStore.Exists
internal/secrets/keystore.go: KeyStore.Path
```
(15 listed — `EnumValues`/`ResetRecent` make 16 with the count below; see
"Two entries needed no edit" for why only 14 files changed.)

## Outcome

**Deleted (3), each with its own dedicated test removed alongside it:**

| Function | Reason |
|---|---|
| `internal/money/money.go: Sum` | Zero callers anywhere, including reflection/template `FuncMap` registration (checked — no `Sum`-like helper registered). Its one test assertion in `money_test.go` removed with it. |
| `internal/print/escpos.go: Doc.Validate` | Zero callers; removing it also dropped the file's now-unused `fmt` import (required, not optional — confirmed 0 remaining `fmt.` uses in the file). |
| `internal/db/db.go: BaselineStatementsFor` + `internal/db/baseline_statements_test.go` (76 lines) | Its doc comment named `internal/pages`' `seedCountrySettingsTable` as the sole consumer; that function no longer exists (only in the deleted comment's own generation and in `setup_page_test.go:38`'s "since-removed" note). Its replacement, `openPagesTestDB` running the real migration set, is a strictly stronger guarantee than the deleted test's "extracted statements reproduce a full `Open`" — no coverage lost. |

**Kept with a verified doc comment (11), baseline lines unchanged:**

`controlServer.LastInputAt`, `SetAndroidBridge`, `NewI18n`, `SweepPrinters`,
`NewSigningDeviceCredentialStoreAt`, `SigningDeviceCredentialStore.Path`,
`pihealth.CheckNow`, `RenderKitchenTicketText`, `KeyStore.Exists`,
`KeyStore.Path` — each comment states which layer's tests reach it and, for
the ones with a plausible-looking "should be wired" shape, an explicit note
that wiring it is a product call this no-behaviour-change slice deliberately
does not make (most notably `RenderKitchenTicketText`: no kitchen-ticket
preview page exists yet, unlike the receipt designer's live `RenderText`
preview — wiring one, or deleting the function with its two parity tests, is
future product/BA work, not decided here).

One correction along the way: `controlServer.LastInputAt`'s *old* comment
claimed `GET /diagnostics`' `last_input_age_seconds` was "built on top of"
this accessor. It is not — `handleDiagnostics` reads `lastInputAt`/`haveInput`
directly under the same lock. New comment states the true (test-only) call
path.

**Two entries needed no edit** — `internal/diagnostics/events.go: EnumValues`
and `internal/logging/logging.go: ResetRecent` already carry adequate,
verified "why no caller" doc comments on `main` (the former even cites this
guard's own sanctioned-false-positive precedent). Confirmed correct as-is
rather than silently skipped.

**One unrelated, pre-existing stale baseline line removed alongside this
slice:** `internal/plugins/marketplace/client.go: Client.AckDownload`. A
separate, already-merged PR (#1246, `fix/2381-ack-download`) wired it into
`internal/plugins/installer_marketplace.go:104` in production code, but
left the baseline stale. Per `guard-deadcode-baseline.sh`'s own design, a
stale "burned down" entry can never fail the guard (informational-only), so
bundling this one-line correction here is CI-neutral; splitting it into its
own PR would cost more than the fix is worth.

`scripts/ci/deadcode-baseline.txt`: 71 → 67 lines (3 genuine deletions + the
1 unrelated stale-entry correction).

**One additional, zero-behaviour comment fix** in `mobile/mobile.go`
(`SetBluetoothBridge`): its doc comment still said "nothing calls this with
a real implementation yet" (citing ut-docs#1731), which the independent
review's investigation of `SetAndroidBridge`'s own caller chain showed is now
false — `TillService.kt:115` calls it in production. Corrected in the same
commit since it directly contradicts the comment this slice just verified
and added two functions away.

## Independent review (Opus, isolated worktree)

All 12 factual claims (one per touched entry, plus the bundled baseline
correction) independently re-derived from source, not from the diff's own
prose — repo-wide caller greps including template/`FuncMap` registration
checks, reading the real `handleDiagnostics`/`DiscoverPrinters`/`Load`/
self-heal code paths directly rather than trusting the doc comments'
description of them. Specifically checked the two traps this card's own
prior slices had already fallen into once each: a "whole feature is dead"
overclaim (none found — every kept function's contrast claim, e.g. fiscal
`Exists` being self-heal-live while `Path` isn't, held up) and a named
symbol that doesn't actually exist (none found).

Ran the full gate independently in its own worktree: `gofmt -l .` clean,
`go build ./...`/`go vet ./...` clean, `go test ./...` — 60 packages `ok`,
0 `FAIL`, `golangci-lint run ./...` — 0 issues, `guard-data-access.sh` green.
`guard-deadcode-baseline.sh` itself fails in this sandbox on a pre-existing,
already-documented limitation (no GTK/WebKit dev headers to type-check
`cmd/unitill-desktop` under `-tags=desktop`) — not a finding; a substitute
run (same tool, roots `. ./cmd/unitill-uninstall`, no desktop tag) showed
**zero new unreachable functions** beyond the baseline.

**No blocking findings.** One optional, non-blocking note (the `mobile.go`
comment above) — folded into this commit rather than deferred, since it was
zero-risk and directly on-topic.

## Verified beyond automated tests

- Confirmed via `git diff` that every one of the 11 "kept" files' changes
  are comment-only — no signature, logic or behaviour touched.
- Confirmed no `web/`/template file appears anywhere in the diff (backend
  doc-comment/deletion-only slice) — the UI/UX checklist and user-manual
  rule genuinely don't apply, checked rather than assumed.
- Scanned all added/removed lines for a real client/shop name or a
  secret-shaped literal (this slice touches fiscal-credential and keystore
  code specifically) — clean; every `key`/`secret`/`credential`/`token`
  occurrence in added lines is prose inside a comment.
- Re-ran `go test ./...` a second time after the post-review `mobile.go`
  comment fix and the review's suggested commit-message content — still
  60 packages `ok`, 0 `FAIL`.

## Safe-to-merge verdict

**Yes.** No code changes required by review; the only pre-merge work was
process (this record, and a real commit message in place of the dev
subagent's WIP snapshot) — both done in this commit.
