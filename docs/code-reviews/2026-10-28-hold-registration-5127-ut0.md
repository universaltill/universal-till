# Review: hold store registration while a my. pairing is held (ut-docs#5127, UT-0)

## What shipped

Path B of `ut-docs/reference/investigation-5127-duplicate-shops-on-repair.md`:
during the my.-first (cloud-paired) first-boot wizard, a pending base plugin
(a language pack core does not bundle, a German fiscal plugin) installed
before the pairing was saved. The install called `enroll.EnsureRegistered`,
which registered a till-born store under the boot device id. `Commit` then
replaced that identity with the paired one and the till-born store stayed
behind as a silent orphan.

- `internal/enroll/enroll.go`: `HoldRegistration`, `ReleaseRegistration`,
  `RegistrationHeld`. While held, `EnsureRegistered` returns the effective
  config and does not register. The hold has a deadline (35 min, longer than
  the wizard's 30-minute pairing hold) so nothing can strand it. `Init`
  releases it. `RegisterNow` is deliberately not gated (Settings only,
  reachable after setup).
- `internal/pages/setup_cloud_pair.go`: the held pairing takes the hold in
  `hold()`, releases it in `drop()`, `finish()` and on expiry in `current()`.
  `hold()` is a no-op once setup has finished (nonce cleared), so a late pair
  call cannot strand it. All under `s.mu`.
- `internal/pages/setup_page.go`: a standalone (non-cloud) setup submit drops
  a stale hold before its installs; after a successful commit the base-plugin
  retry is nudged so a plugin left pending installs under the paired
  identity at once. Not nudged when saving the pairing failed (the retry
  would register a till-born store the re-pair would orphan).
- `internal/pages/setup_tax_catalog.go`: the wizard's fiscal-plugin install
  queues without a doomed foreground attempt while registration is held.

## Tests (written first)

- `TestCloudSetupDoesNotRegisterATillBornStoreBeforeCommit` (pages): a held
  pairing, a pending `language/de`, a final cloud post; counts
  `/v1/stores/register` hits at the moment of `Commit`. Failed first with
  "called 1 time(s)" against a stub `RegistrationHeld`, passes with the fix.
- `TestCloudPairHoldFollowsTheHeldPairing`: released by unpair, expiry and
  `finish()`; never taken after setup finished.
- `TestCloudSetupNudgesBasePluginRetryAfterCommit`: the retry is nudged after
  Commit (channel drained right before Commit so only that nudge counts).
- `TestEnsureRegisteredHonoursHoldRegistration` (enroll): no register call
  while held; one after release.

## Review

Built by Sonnet. Independent review by Opus in a read-only pass.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | Hold could be stranded: a pair call finishing after `finish()`, and expiry only checked lazily in `current()`; Hold/Release outside `s.mu` | medium | Fixed: deadline in enroll, `hold()` no-op after finish, calls under `s.mu`; test added |
| 2 | A standalone submit ran with a stale hold, skipping the ADR-0071 opt-in registration | low-medium | Fixed: `cloud.drop()` when the post is not cloud-paired |
| 3 | The post-finish nudge fired even when Commit failed, bringing the orphan back | low-medium | Fixed: nudge only when the pairing was saved |
| 4 | Tests did not cover the nudge, release on expiry/unpair, or the late pair call | low | Fixed: two tests added |
| 5 | German fiscal install made a doomed attempt while held; comment wording on RegisterNow | nit | Fixed |

No other caller of `RegisterNow`/`EnsureRegistered` can run in the held
window (checked: the enrol device loop only registers a device under an
existing store; `resolveAndInstallBasePlugin` uses `Effective`; the
Settings and plugin-store callers are post-setup).

## Not covered

- When saving the pairing fails after the wizard (`cloudPairFailed`), the
  till is unregistered and the next lazy registration creates a till-born
  store; the welcome page's "pair again in Settings" then hits path A. That
  is the Settings move (UT-1 / CL-*), not this card.
- Not driven on a device; this is a wizard-ordering fix covered by handler
  tests against a stub cloud.
