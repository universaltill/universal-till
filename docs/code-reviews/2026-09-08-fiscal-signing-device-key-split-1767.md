# ut-docs#1767 — fiscal signing-device posture key splits per country (ADR-0083)

Date: 2026-09-08
Branch: `fix/1767-fiscal-signing-device-key-split-by-country`
Review: one independent subagent, fresh context, Opus (`complexity:hard`),
isolated worktree.

## The bug this closes

`internal/fiscal.EvaluateGate` already took `country` as a parameter, but
until this change it read ONE country-agnostic settings key for "is this
shop's mandated signing device configured" — `fiscal.signing_device_configured`
— for both hard-gated markets (Germany's TSE, Turkey's YN ÖKC). ADR-0081 put
them on that single key; ut-docs#1750 found the consequence: because the row
a market's gate reads was selected by `store.country` (ordinary,
manager-writable shop config), confirming Turkey's ÖKC device and then
flipping `store.country` to Germany made a German till read `Allowed` with no
TSE at all. #1750 shipped a write-path mitigation (owner-auth required to
change country while a device is confirmed, shared key cleared on change);
both of its independent reviews flagged that as a patch, not a fix to the
data model. This card (ADR-0083) is that fix.

## What shipped

- `internal/fiscal.SigningDeviceConfiguredKey(country)` /
  `SigningDeviceFailingSinceKey(country)` replace the flat
  `KeySigningDeviceConfigured`/`KeySigningDeviceFailingSince` constants
  outright (removed, not deprecated) — `fiscal.signing_device_configured.de`
  / `.tr`, independent rows. `EvaluateGate` resolves via its existing
  `country` argument; no caller of `EvaluateGate` itself changes.
  `fiscal.KeySystemOfRecord` and the three override keys stay global,
  deliberately (short-lived, owner-granted, `MaxOverrideMinutes`-capped —
  a much smaller blast radius than the two keys that decide Allowed/Blocked
  with no time bound).
- Migration `011_split_fiscal_signing_device_keys_by_country.sql`: renames
  an upgraded till's existing flat row onto its currently-declared
  `store.country`, mirroring migration 009's DELETE-then-UPDATE
  idempotent/multi-till-safe shape exactly, with a NULL-key guard (`settings.key`
  is a bare `TEXT PRIMARY KEY`, SQLite permits NULL) proven load-bearing by
  test.
- Every writer resolves the country-scoped key: `fiscal_device_hook.go`,
  `fiscal_device_page.go` (both already gated by `fiscalDeviceMarketActive`,
  TR-only), `fiscal_api.go`, `settings_page.go`'s generic upsert/save path
  (wire-level key names unchanged — `resolveFiscalPostureKey` translates the
  flat logical name, or an explicit per-country row, to the real storage key,
  keeping the existing owner-only permission check and audit trail on both
  shapes), and `fiscal_country_change.go` (owner-auth-required-to-change-
  country guard and clear-on-change behavior kept, retargeted at the country
  being LEFT rather than removed).
- `setup_tse.go`'s `finishTSEProvisioning` resolves against the TSE
  provisioning record's own `Country` field, not the live `store.country`
  (see review finding below — this is NOT what the first draft did).
- `cloudsync_wire.go`'s `set_setting` directive explicitly refuses both
  posture keys (flat or per-country) rather than routing through the
  settings-page resolution — it has no HTTP session to authorize against
  (see review finding below).

## Independent review

One Opus subagent, fresh context, isolated `git worktree` (never shared the
orchestrating session's checkout), read `docs/arch` + this repo's own
`CLAUDE.md`, ran the full gate itself, and did its own independent
revert-restore verification of the load-bearing independence test
(`TestEvaluateGate_PerCountryKeysAreIndependent`) before looking for
anything else. It was explicitly briefed NOT to rubber-stamp and to go
beyond what a prior Sonnet-level tester pass had already checked.

**Verdict: FAIL as submitted, PASS after one HIGH fixed in the same pass.**

### HIGH — `finishTSEProvisioning` resolved against the live country, not the provisioned one — fixed

The ADR's own first draft asserted every writer was safe to resolve against
`d.CurrentState().Country` because each one already fires under a
pre-existing country guard, and named `setup_tse.go`'s DE-only wizard gate as
covering `finishTSEProvisioning`. That gate covers the wizard's *kickoff*,
not this function: completion is asynchronous (the cloud's `fiscal_tse_ready`
directive, or the `StartTSEProvisionRetry` background ticker), and
`store.country` is manager-writable in between with **no owner check**,
because `fiscal_country_change.go`'s guard only demands owner authority once
a device is confirmed — precisely what has not happened yet while
`awaiting_ready`. Reachable sequence:

```
set up DE, kickoff accepted (awaiting_ready)
-> manager sets store.country=TR (allowed: no posture proven yet)
-> the cloud's fiscal_tse_ready lands
=> a German TSE credential stamps fiscal.signing_device_configured.tr = true
=> Turkish till, fiscal.Allowed, no YN ÖKC has ever answered
```

This is the exact ut-docs#1750 end state, reopened through a different
writer — the per-country split only closes that class if every writer
genuinely names the market it proved something about.

**Empirically confirmed before the fix**, new test
`TestApplyFiscalTSEReady_WritesProvisionedMarketsRowNotCurrentCountrys`:
seeded a `DE` provisioning record, changed `store.country` to `TR` mid-flight,
ran `applyFiscalTSEReady` — failed with `a German TSE credential set Turkey's
posture row to "true" — no ÖKC has ever answered on this till` against the
as-submitted code.

**Fix**: `finishTSEProvisioning` now resolves the key from
`loadTSEProvisioningState(...).Country` (the same field
`tseProvisioningDismissBlocked` already reads, for the same "never a second
hardcoded DE" reason), falling back to `tseProvisionCountry` only once that
record is cleared by the idempotent re-serve path (i.e. after the first
successful write already used the right key). All 14 TSE tests pass after,
including the pre-existing idempotent-retry test that exercises the
fallback. ADR-0083 amended (point 3) to record the corrected reasoning
rather than leave the wrong claim standing.

### MEDIUM — the cloud `set_setting` directive didn't go through the same key resolution — fixed

ADR-0083's first draft (point 4) and `settings_page.go`'s own comment both
claimed the cloud directive "still sends" the flat logical names through the
same resolution as the HTTP upsert handler. It doesn't — `cloudsync_wire.go`
wrote `d.Settings.Set(ctx, key, value)` verbatim. Consequences: a remote
`set_setting fiscal.signing_device_configured=true` becomes a silent no-op
against a dead key (fail-closed, merely wasteful) — but a remote **`=false`
posture revocation is also a no-op**, and that direction is fail-**open**:
the real per-country row keeps reading `true` and the till keeps selling.

Fixed with `rejectRemoteFiscalPostureWrite` (`cloudsync_wire.go`): the
directive now refuses both posture names outright with an honest error,
consistent with these keys being owner-only everywhere they're reachable
from an HTTP session — this directive has no session to check a role
against, so refusing is the safer default (matches ADR-0048's fail-closed
posture for a compliance-critical flag). Confirmed with a targeted
revert-restore proof of the new `TestRejectRemoteFiscalPostureWrite`. ADR-0083
amended (point 4) to match.

### LOW findings — accepted as-is, not fixed

- SQLite's `trim()` strips ASCII space only (Go's `strings.TrimSpace` is
  broader) — a `store.country` carrying a tab/newline would compute a
  slightly different migrated key than the Go key functions would for the
  same raw value. Not fixed: `RequiresHardGate` is exact-match strict
  (`"DE"`/`"TR"` only), so any such value already has the gate switched off
  at runtime regardless of which row the migration puts it on — the mismatch
  is inert, and every actual writer of `store.country` in this codebase
  Go-trims before it's ever stored. Tightening the migration SQL for a case
  that cannot be reached in practice would add noise to an already carefully
  commented migration.
- `resolveFiscalPostureKey` accepts an empty country suffix (posting the
  bare wire name with a trailing dot) and creates an inert, unreadable
  settings row. Owner-only, cosmetic, no gate ever reads it.
- A handful of comments elsewhere still name the flat key in passing
  (`fiscal_api.go`, `setup_tse.go`, `setup_page.go`, `tse_credential_store.go`)
  — none load-bearing, all read as the logical/conceptual name rather than a
  literal storage key.

### Explicitly checked and clean

- Every other call site of the removed flat constants: none remain (the
  constants were deleted outright, so a stale reference would fail
  `go build`, which is clean) — comments, scripts, `web/locales/*.json` all
  checked separately and none reference them.
- `resolveFiscalPostureKey`'s permission/audit/storage switches are
  consistently keyed off the same resolved values; `POST /api/settings/save`
  needs no resolution (it writes no fiscal key at all).
- Migration's NULL-key guard holds — proven load-bearing by the reviewer's
  own second revert-restore proof (dropping the guard reproduces the NULL-key
  write the comment predicts).
- `fiscal_country_change.go`'s retargeting is correct across all three HTTP-
  reachable writers of `store.country`; the setup wizard remains correctly
  excluded (still gated on `NeedsFirstBoot`, no posture to protect yet).
- No `os.MkdirAll`/cwd-relative-path bug class present — this diff does no
  disk I/O.
- No real client/shop name or secret-shaped literal anywhere in the diff.
- No UI/HTML/i18n surface touched — confirmed by file list and a clean
  `guard-i18n.sh` run.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./...` — full repo, 51 packages, 0 failures (both before and
  after the two fixes above).
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-migration-version-collision.sh`
  — all pass.
- Independent revert-restore TDD proofs, done separately by the Tester pass
  and by the Reviewer subagent, for: the core independence test (all four
  directions — TR/DE configured, TR/DE failing-since), the migration's
  NULL-key guard, the `finishTSEProvisioning` fix, and the cloud-directive
  refusal.
- ADR-0083 (`ut-docs`, PR #1785) amended to correct the two points the
  review found wrong before either PR merges — document-first stays honest
  about what was actually built, not what the first draft assumed.

## Explicitly out of scope / deferred

- ADR-0081's vocabulary and ADR-0048's gate policy — untouched, not
  reopened.
- Splitting the override keys (`KeyOverrideUntil/Reason/Actor`) per country —
  deliberately deferred (ADR-0083 point 2); checked for a practical
  cross-country leak during review and found none (`EvaluateGate` hits
  `BlockedNeverConfigured` before ever reading the override keys once a
  country's own `configured` row is false, which is what a country change
  always leaves the destination market at).
- Tab/newline-in-`store.country` migration edge case (LOW finding above) —
  inert, not worth the added SQL noise.

## Verdict

Safe to merge. One review round; the round found one HIGH (a live
compliance-gate bypass reopened through a writer the ADR's own first draft
got wrong) and one MEDIUM (a remote-directive fail-open on revocation),
both fixed and re-verified in the same pass, both mechanical and narrowly
scoped to what was found. No second review round needed — neither fix
touched anything the first pass hadn't already exercised.
