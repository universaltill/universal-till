# Review: the setup wizard pre-fills no till name and requires one (ut-docs#4146)

- **Date:** 2026-10-13
- **Card:** universaltill/ut-docs#4146 (rescoped slice; split-outs #4271, #4272, #4273)
- **Branch:** `feat/4146-wizard-requires-till-name`
- **Author:** Dev subagent (Opus 5.5), lane `lane:cloud-41`
- **Reviewer:** independent Fable subagent in its own worktree (a different model from the author)

## What shipped

Owner rule (2026-10-11): "we shouldn't give any default name to the shop or
till". The shop-name half already shipped (#3096). This change covers the till:

- `web/ui/pages/setup.html`, step 4:
  - The till-name field has no "Till 1" (`setup.till_name.default`) value. It is `aria-required`.
  - Next stays disabled until both the shop name and the till name are non-blank.
- `internal/pages/setup_page.go`, `POST /api/setup`:
  - The till name goes through `validateTillName` with the other pre-persist checks, so a refusal saves nothing.
  - A refused name re-renders step 4 with the existing key `tills.pairing.error.name_required_field` (no new locale keys, so no language-pack follow-up).
  - The trimmed name is always saved to `till.name`.
  - A refused till name is not echoed back into the field (same rule as a refused shop name). Any other error keeps it.
- Tests:
  - Every Go test and e2e flow that completes the wizard now types a till name.
  - `login.spec.ts` asserts the field starts empty and that Next is gated on it.
  - `setting_scope_test.go` lists the new constant as an i18n message key, not a settings key.
- Manual: `web/help/{en,de,tr,fa,ar}/users.md` item 4 now says the till name is required, has no default, and can be changed later in Settings. `web/help/img/manifest.json` is regenerated (surface hash and `users` topic hashes).

Out of scope by design, carried by the split-out cards:
- The cloud's placeholder name and "Unnamed shop" display (#4271).
- Registration waiting for a shop name (#4272), where the wizard's step order is the open design question.
- Existing installs: the "My Store" migration, the admin-login prompt, the `tillNameOrDefault` "Till 1" fallback and `display.md`'s "defaults to Till 1" sentence (#4273).
- The bare `POST /api/auth/setup` fallback.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A till name refused for a control character (a pasted tab) or length was echoed back. The field looked filled and every resubmit said "required". | **Fixed.** The refused till name is not echoed (`errKey != keyTillNameRequired`). The test asserts an empty field in the refusal cases and was seen failing first (control_char and over_60_runes). |
| 2 | nit | `web/help/*/display.md` item 9 still says the till "defaults to 'Till 1' until you set one". | **Deferred to #4273.** Still true for existing installs, where that fallback lives. A comment was added there. |
| 3 | nit | No test checked that the till name survives a re-render for another error. | **Fixed.** `TestSetupWizardRerenderKeepsTillName` (PIN mismatch). |

The reviewer also checked these paths and found no problem:
- The tax-plugin pending/skip redirects, the DE TSE step-3 errors and the language reload.
- Demo mode, and join/pairing (`/api/setup/join` has its own required field).
- Escaping of the re-rendered value, and the Alpine `x-init`/`x-model` order.
- `maxlength` (UTF-16 units) against the 60-rune limit.
- The meaning of the de/tr/fa/ar help sentences.

## Verified

- **TDD re-verified by the reviewer in its own worktree:**
  - Production `setup_page.go` reverted → `TestSetupWizardRefusesBlankOrInvalidTillName` fails in all 5 cases (303 → `/import?welcome=1`).
  - `setup.html` reverted → `TestSetupWizardTillNameFieldHasNoDefault` fails.
  - Both restored → pass.
- **Gate after the last edit:**
  - `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), `golangci-lint run ./...` (0 issues).
  - `go test ./...` (see the PR for the result).
  - Every ci.yml guard touching these paths passes: i18n, help-drift, help-topics, no-showmodal, osk-loaded, no-inline-handlers, compliance-claims, competitor-naming, data-access, the e2e guards, autofill, page-http-error, kiosk-engine, core-neutral, htmx, netaccess, pipefail, card-data and migration-collision.
  - `run-docs-shots-guard.sh` passes with the regenerated manifest.
- **e2e:** `--project=auth tests/login.spec.ts`, 16/16 passed (Dev run).
- **docs-shots:** `make docs-shots` ran 120/120. Its PNGs were **not** committed: every page in this container shows a "Storage almost full" status chip (the container's disk, not the product). The wizard is not a screenshotted surface, and the `users` text change does not alter `users.png`. Only the manifest's hashes changed for real, so only `manifest.json` is committed. The docs run types "Till 1" so screenshots stay byte-stable.
- **Visual:** I read the regenerated `en/sell.png` against the committed one. They are identical apart from the container's storage chip. The wizard's step 4 was not screenshot-checked. The only change there is a field that starts empty and a Next button that is disabled until it is filled, and the e2e covers both.

## Verdict

Safe to merge.
