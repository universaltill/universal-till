# Review — a till registers with the cloud only once its shop is named (ut-docs#4272)

Date: 2026-10-15 · Branch: `feat/4272-register-requires-shop-name` · Built by Opus 5.5, reviewed by Fable (independent).

## What shipped
- `internal/enroll`: `RegisterNow` no longer calls `POST /v1/stores/register` for a main/standalone till whose shop name is blank or a placeholder (`config.IsPlaceholderStoreName`). It returns the new `ErrStoreUnnamed` instead. `AwaitingShopName` reports that state. The replica path (through the main till) and the signing-key fetch are unchanged.
- Settings → Till registration → **Register now** on an unnamed shop shows the translated `settings.enrol.name_required` with a link to the Shop name card (`/settings#settings-store-name`). New key in en/ar/fa/tr. de/es/pt follow up in their pack repos.
- Setup wizard step 3 (DE tax plugin): **Install** on a still-unnamed first boot queues the spec (`tax_plugin_pending`) rather than registering an unnamed shop.
- `POST /api/setup`: after `store.name` is saved, `installBasePluginsForSetup` attempts **every** pending spec, so the queued fiscal plugin installs at the end of the wizard. Anything still pending nudges the background retry at once.
- Help: `claim.md` (en/de/tr/fa/ar) gains the "Name your shop first" case.
- e2e: the three diagnostics-project specs that press Register now name the shop first (`nameShop` helper).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | New core key: the de/es/pt packs need follow-up PRs, or `lang-pack-drift` goes red on `main` | Fixed: pack PRs opened in the same cycle |
| 2 | should-fix | The queued fiscal plugin shares the 5 s end-of-wizard budget. On a slow link it would then wait for the 5-minute retry tick | Fixed: `nudgeBasePluginRetry()` when anything stays pending (test asserts the nudge; it fails without the change) |
| 3 | should-fix (copy) | Step 3 says "Still installing … in the background" while the install actually waits for the final step | Accepted: it installs at the end of the wizard or by the retry, and the note says "you can continue". A dedicated string would add pack churn for little gain |
| 4 | nit | The `ErrStoreUnnamed` row in `enrolFailureKey` is unreachable from Pair | Accepted: defensive |
| 5 | nit (pre-existing) | Pending-list load/save is not atomic between the wizard and the retry tick | Accepted: no new window (#1110, #1117) |
| 6–7 | nit | A credential-less retry tick during steps 3–4, and a language spec with nothing published now drops at the end of the wizard | Accepted: bounded, same outcome |
| T1 | blocker (Tester) | `diagnostic-mode-indicator-2169`, `registration-card-paid-4285` and `subscription-panel-4286` registered an unnamed fresh till and failed | Fixed: the specs name the shop via `nameShop` first |

Caller audit (reviewer): `register()` is reached only through `RegisterNow`. Every `EnsureRegistered` site already fails soft without credentials. Pair and claim-code never register a store. Env-configured tills read as registered.

## Verified beyond unit tests
- TDD re-verified by the orchestrator: with the `ErrStoreUnnamed` branch swapped back to `register(…, "")`, `TestRegisterNowRefusesUnnamedShop` fails. Restored, it passes. Removing the nudge fails the offline base-plugin test.
- Real driven run (Playwright, Chromium): `diagnostics` project 7/7 passed. The first run, before the spec fix, rendered the new message and link in `#enrol-msg` ("❌ Name your shop first … [Shop name] → /settings#settings-store-name"). The `auth` project (first-boot wizard + PIN flow) passed 47/47.
- Not looked at visually: the step-3 note under RTL. No template changed, and the Register-now span sits in an existing message slot.

## Gate
gofmt clean; `go build ./...`; `go vet ./internal/...`; golangci-lint 0 issues; `go test ./...`; every guard in `ci.yml` passes except two that fail only from this container's environment and are left to CI: `guard-deadcode-baseline` (its tool is built with Go 1.26, the repo needs 1.27) and `guard-shellcheck-version` (no shellcheck installed; no shell script changed).

## Verdict
Safe to merge, with the de/es/pt pack PRs landing right after core.

Deferred: a friendly "name your shop" message on a plugin-store install from an old unnamed till (#3541 area). Asking existing installs for a name is #4273.
