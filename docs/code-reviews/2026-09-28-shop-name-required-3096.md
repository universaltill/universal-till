# Review: the setup wizard requires a real shop name (ut-docs#3096)

- **Branch:** `fix/3096-shop-name-required`
- **Author:** Opus 5.5 (lane:cloud-24)
- **Reviewer:** Fable, an independent subagent in its own worktree

## Scope

ut-docs#3096 asks for three things across the till, the cloud and my.: a required shop name at setup, a prompt for legacy shops, and edit plus sync everywhere. This branch is the **till-side setup and enrolment slice** (AC1 and the wizard-refusal test from AC5). The rest was split into separate cards:
- ut-docs#3114: the legacy "Name your shop" prompt;
- ut-docs#3115: editable on the till, with two-way sync to the cloud;
- ut-docs#3020: the kiosk's stale name.

## What shipped

**Root cause.**
- Step 4's shop-name field was optional. A blank POST skipped the `store.name` write.
- Migration 001 seeds `store.name = 'My Store'`, so a shop left unnamed kept that seed.
- Since #2776, registration has sent the live `store.name`, so the seed is what reached the cloud.
- Every first-boot path goes through this wizard: Android, Windows, Pi and desktop.

**Fix.**
- `internal/config`: new `DefaultStoreName` constant and `IsPlaceholderStoreName`. The latter matches blank, "My Store" and the cloud's "Universal Till store", trimmed and case-insensitive.
- `POST /api/setup` refuses a blank or placeholder name before anything is saved. It also refuses this locale's `setup.store.placeholder` text. The refusal re-renders the wizard on step 4 with the new key `setup.error.store_name_required` (en/ar/fa/tr in core; de/es in the pack PRs). The refused name is not echoed back, so Next stays disabled.
- Every error re-render now keeps the typed shop and till names. Before, a PIN typo dropped them, and finishing then left the shop as "My Store".
- `setup.html` step 4: Next is `:disabled` while the name is blank. The field uses `aria-required`, not native `required`: a native `required` on an `x-show`-hidden input would silently block the step-8 submit. The Alpine value is seeded from the input's DOM value, not interpolated into a JS string, so apostrophes are safe.
- `enroll.shopName` never returns a placeholder; with no real name it returns "". Registration is never blocked, so offline-first and plugin installs are unaffected.
- The manual: `users.md` item 4 in all five help languages. `manifest.json` topic hashes were refreshed for `users` only; `/setup` is not a screenshotted route (`Docs-Shots-Unchanged: true`).

**Dropped from the BA note:** the plan was to stop `SaveRuntimeConfig` persisting the env default at boot. It would change nothing, because migration 001 already seeds the value, and shipped migrations are frozen (ADR-0100).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor (pre-existing) | `POST /api/auth/setup`, the bare first-boot fallback, completes setup with no shop name. It is not reachable from the UI: `/login` redirects a first-boot till to `/setup`. | Filed as ut-docs#3116 |
| 2 | nit | The placeholder-text check covers only the request locale's placeholder; a shop genuinely called "My Store" is refused. | Accepted: the card's product decision |
| 3 | nit | On an error re-render, a deliberately blanked till name comes back as the "Till 1" default. | Accepted: harmless, same as the first render |

The reviewer also checked the following and found no issue:
- html/template escaping of the echoed names (`Joe's "Corner" <b>&`);
- Alpine init order (`x-init` before the child's `x-model`);
- the on-screen keyboard dispatching `input` events, so Next enables on a kiosk;
- every other e2e wizard driver fills a name;
- the join and demo paths don't set `setup.completed`;
- no SQL, migrations, file writes or RTL CSS in the diff.

## Verification

**TDD.** The reviewer reverted the four source files, kept the tests, and re-ran them:
- `TestRegisterNowNeverSendsPlaceholderName` fails with `store_name = "My Store", want ""`.
- `TestSetupWizardRefusesBlankOrPlaceholderStoreName` fails with `code=303 … want a 200 wizard re-render`.
- `TestSetupWizardPINErrorRerenderKeepsStoreName` fails with `lacks value="Corner Café"`.
- The config test fails to compile.

With the source restored, all four pass. The author saw the same failures before writing the fix.

**Gate.**
- `go build ./...` passes, and `go test ./...` passes for the whole module.
- `golangci-lint run ./...` reports 0 issues, and `gofmt` is clean.
- All `build`-job guards pass, except two that are environment-only:
  - `guard-deadcode-baseline` also fails on `main` here. It skips `cmd/unitill-desktop` without GTK headers, and `logging.Stderr` is used only there.
  - `guard-shellcheck-version`: there is no shellcheck binary. No shell script was touched.
- Playwright `auth` project: 30/30 passed. `login.spec.ts` now asserts that Next is disabled when the field is empty or holds only spaces, and enabled once a name is typed.

**Looked at (Chromium, real server).**
- Step 4 at 1024×600 and 360×800, in en and fa (RTL): Next is visibly disabled when empty and enabled after typing "Joe's Café".
- The refusal re-render at 1024×600 (en): the message sits above the heading, the till name is kept, and there is no overlap.
- Not checked: real touch hardware (only the on-screen keyboard's code path was reviewed), the dark theme, and the de/es packs, whose keys aren't in core.

## Verdict

Safe to merge. The new core key means the language packs follow in the same cycle: `ut-plugin-language-{de,es}` PRs on branch `i18n/3096-store-name-required`.
