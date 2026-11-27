# Review: restored main till offers TSE Retry when its credential did not travel (ut-docs#7050)

Date: 2026-11-25 · Lane: lane:cloud-41 · Branch: `fix/7050-tse-credential-restored`
Complexity: medium · Built by Sonnet · Reviewed by Opus 5.5, in a fresh context

Pre-flight: Any change: review record = this file; help = `web/help/en/backups.md` updated (English only; `web/help/img/manifest.json` hash refreshed, no screenshot change); en.json = one new key `settings.tse.credential_missing` with `en.meta.json` metadata; migrations/catimport = n/a; `gofmt -l .` empty; `go build ./...` OK; `go vet ./internal/pages` clean; `golangci-lint run ./...` 0 issues; `go test ./...` all ok (internal/pages and internal/plugins re-run alone with a longer `-timeout`: 713 s / 976 s); `go test -race ./internal/pages -run 'TSE|Tse'` ok; 144 of 146 build-job guards pass, 2 environment-only (`guard-catimport-version.sh`: no catimport tags fetched; `guard-shellcheck-version.sh`: no shellcheck binary, so shellcheck not run locally).

**Verdict (2026-11-27, after two fix rounds): ship.** Round 1 verdict was "ship after fixes"; the card ran out of its time budget before the fix round, which a later `:41` run finished under the PR-SWEEP #5153 exception (see "Fix rounds" below).

## Change

- New shop-wide settings row `fiscal.signing_device_credential_held`, written in `finishTSEProvisioning` right after the "configured" audit (fresh fetch and already-stored path).
- Boot `reconcileTSECredentialMarker` (`internal/pages/setup_tse.go`, called from `init.go` before the TSE retry service): replica → nothing; credential readable → backfill the marker; marker + `fiscal.signing_device_configured.de` + no provisioning record + no readable credential → save new status `credential_missing` and audit it. Never flips the configured flag, never touches the ADR-0048 gate, no network.
- Settings → Receipt signing (TSE) shows `credential_missing` with an inline four-field identity form (wizard labels, `parseTSEIdentityForm`, `setup.error.tse_*`). Posts `tse_identity_form=1` to the existing Retry route; invalid input → 422 in-place re-render; without the flag, behaviour unchanged (409). Retry re-runs the kickoff; ut-cloud re-mints and revokes the old key (ADR-0053), no cloud change.
- Help: `backups.md` gives the self-service step instead of "contact support".

## Tester (independent, Sonnet)

Mutation checks: removing the marker write fails 2 tests; skipping the `credential_missing` persist fails 3. Driven run of the real binary in the restored state: boot set `credential_missing`; block renders at 1024x600 and 360px (44px inputs, no horizontal scroll, status bar not inert); invalid tax number → 422 in place, values kept, `aria-invalid`, focus on the alert; valid identity → one POST, `pending_kickoff`, sale screen fine, no modal; `fa` RTL mirrors, tax field `dir=ltr`. Emulated, not hardware-verified (touch, OSK, real phone).

## Findings (Opus 5.5) — open, to fix before merge

1. **Medium** — `reconcileTSECredentialMarker`: any read error counts as "missing" (`held := lerr == nil && ok && len(cred) > 0`). A transiently unreadable `fiscal/` at boot records `credential_missing`, which never self-clears and cannot be dismissed for DE; Retry would revoke a working key. Fix: only NotExist/decode errors count as missing, other errors log and return; if the credential reads fine and the record is `credential_missing`, clear it and audit. Tests for both.
2. **Medium/low** — marker never cleared and synced shop-wide: a shop that later switches to a manually configured own device, then promotes a replica, gets an undismissable notice whose Retry mints an unused cloud TSE. Fix: clear the marker when the configured flag is set manually (owner upsert, `set_setting` directive); refuse writing the marker through the free-text upsert like `failing_since`.
3. **Low** — boot-only check (a replica promoted without restart, or a backup taken mid-provisioning, is not flagged). Document as a limitation.
4. **Low** — test gaps: no test for marker + no file + configured flag false → no record; nothing proves `init.go` calls the reconcile (Tester confirmed: deleting the call fails no test).
5. **Low** — identity-form 422 path ignores the `loadTSEProvisioningState` error, and if the record already moved to `pending_kickoff` the error text never renders. Fix: fall through to 409 `nothing_to_retry` when the form isn't needed; 500 on load error.
6. **Nit** — byte vs character length limit (pre-existing in the wizard).
7. **Nit** — marker written for any country, boot check reads only `.de`; add a comment or a per-country key.

Checked and fine: same route, `canPerform(settings)` gate and CSRF as Retry; no identity in audit or logs; escaped template values; i18n/meta, RTL logical CSS, no showModal/native confirm; backward-compatible 409.

## Fix rounds (2026-11-27, `lane:cloud-41`, PR-SWEEP #5153 sweep)

`main` merged in first (conflict only in `web/help/img/manifest.json`: backups topic hash recomputed from the merged markdown, surface record refreshed with `update-docs-shots-surface-hash.sh`; no screenshotted pixel changes).

**Round 1 (Sonnet Dev):**
- Finding 1 fixed: only "no such file" or a decode error counts as a missing credential, and any other read error logs and writes nothing. A `credential_missing` record whose credential reads fine is cleared and audited as `credential_restored`.
- Finding 2: the marker is cleared when the configured flag is written manually, on the owner upsert and on the main till's sync apply. Writing the marker key is refused on the free-text upsert (400), sync apply (`fiscal_not_settable`), `rejectRemoteFiscalPostureWrite` and the `set_setting` directive.
- Finding 3 documented: the boot-only limitation is in the reconcile doc comment.
- Finding 4 fixed: a test for marker + no credential file + configured false (no record written), and the `bootTSECredentialCheck` helper with its tests.
- Finding 5 fixed: a load error returns 500, and the 422 path applies only while the record still needs the identity; otherwise the existing 409 is returned.
- Finding 7 fixed: comment added.
- Nit 6 accepted. The limit predates this change and belongs to the wizard.

**Round 2 review (Opus 5.5, fresh context, round-1 diff only):**
1. Medium: the boot backfill re-armed a marker that a manual write had set to `"false"`, which reopened finding 2. Fixed: the backfill now runs only for an unset or empty marker. Regression test `TestReconcileTSECredentialMarker_DoesNotRearmExplicitFalse`.
2. Low: a no-op re-save cleared the marker. Fixed: the marker is cleared only on a real change of the stored value, in both the upsert and sync apply. Tests cover re-saving the same value and changing it.
3. Low: the init wiring test was a substring check. It now uses go/parser + go/ast and requires a real call that comes before `StartTSEProvisionRetry`. Verified: commenting the call out fails the test, and moving it after the retry service fails it.
4. Nit: added a test that a follower till's upsert never writes the marker locally.

Round 2 was the follow-up the medium tier allows for a Medium finding. Only its fixes were re-verified, with no further review round.

**Verification:**
- Tester (Sonnet, independent, on the round-1 snapshot): `gofmt`, build, vet and `golangci-lint` (0 issues) pass. Full `go test ./internal/pages` passes (510 s), as do `internal/cloudsync` and `-race` on the TSE tests. 68 of 70 build-job guards pass; the 2 failures are environment-only (no catimport tags fetched, no shellcheck binary), and the diff touches neither area. Three mutation checks were spot-checked independently.
- Round 2 Dev: targeted `internal/pages` tests and lint pass. Orchestrator: re-ran the backfill mutation and it fails with "boot backfill re-armed a marker…"; restored, the targeted suite passes (77 s).

Pre-flight (fix rounds): review record = this file. No new en.json keys, no template change. Help unchanged in these rounds (`backups.md` already updated in round 0). Migrations, catimport and SQL: n/a. docs-shots surface record refreshed, no pixel change. Follow-up #7051 (status-bar chip) was already filed.
