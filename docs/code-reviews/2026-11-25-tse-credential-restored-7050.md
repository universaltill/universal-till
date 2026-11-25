# Review: restored main till offers TSE Retry when its credential did not travel (ut-docs#7050)

Date: 2026-11-25 · Lane: lane:cloud-41 · Branch: `fix/7050-tse-credential-restored`
Complexity: medium · Built by Sonnet · Reviewed by Opus 5.5, in a fresh context

Pre-flight: Any change: review record = this file; help = `web/help/en/backups.md` updated (English only; `web/help/img/manifest.json` hash refreshed, no screenshot change); en.json = one new key `settings.tse.credential_missing` with `en.meta.json` metadata; migrations/catimport = n/a; `gofmt -l .` empty; `go build ./...` OK; `go vet ./internal/pages` clean; `golangci-lint run ./...` 0 issues; `go test ./...` all ok (internal/pages and internal/plugins re-run alone with a longer `-timeout`: 713 s / 976 s); `go test -race ./internal/pages -run 'TSE|Tse'` ok; 144 of 146 build-job guards pass, 2 environment-only (`guard-catimport-version.sh`: no catimport tags fetched; `guard-shellcheck-version.sh`: no shellcheck binary, so shellcheck not run locally).

**Verdict: ship after fixes. Not merged** — the card ran out of its time budget before the fix round (see the card).

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
