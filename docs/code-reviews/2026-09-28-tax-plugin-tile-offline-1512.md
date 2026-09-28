# Code review — offline first-boot tax-plugin tile (ut-docs#1512)

- **Date:** 2026-09-28
- **Branch:** `fix/1512-tax-plugin-tile-offline`
- **Author:** pipeline build lane (lane:cloud-24), Opus 5.5
- **Reviewer:** independent subagent, Fable (different model from the author)

## What shipped

Owner decision (2026-09-28, on ut-docs#1512): always show the tax-plugin prompt in the German setup step, even when the till is offline.

- `installableTaxPlugin` gains `Offline bool`. When the tax catalog is unreachable and nothing is cached, `setupInstallableTaxPlugin` returns an Offline tile for a tax-mapped country. Before, it returned `nil`, so the tile never showed and #1506's queue-on-Skip/Next had nothing to attach to.
- `POST /api/setup/tax-plugin` with an Offline match queues `{tax, de}` on the existing #591 pending list (`addPendingBasePlugins`) and redirects with `tax_plugin_pending=1`. It skips the 20 s foreground attempt.
- `setup.html`: the offline note (`setup.tax_plugin.offline_note`), the button label "Install when online" (`install_when_online_btn`), and a "Queued" confirmation after consent (`queued_offline`). Skip/Next already queue the plugin (#1506) and now also work offline.
- Locale keys in en/ar/fa/tr, plus follow-up PRs in `ut-plugin-language-de` and `-es`. The step 8 help sentence is in en/de/ar/fa/tr. The docs-shots manifest was regenerated (no PNG changed; `/setup` isn't screenshotted).

ADR-0025 decision 4 ("fiscal plugins are prompted, never silently auto-installed") still holds: the operator sees the tile and consents, and nothing is queued behind their back.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (pre-existing) | A queued `{tax,de}` is removed without a warning if the catalog later answers with no DE tax listing (`resolveAndInstallBasePlugin` returns nil for "nothing published", and the retry tick then removes it). | Pre-existing #591/#1506 behaviour. Follow-up: ut-docs#3210. |
| 2 | minor–major | The Offline tile shows even when a tax plugin is already installed (sideloaded, or the wizard re-runs after a user reset). Offline there is no listing id for the install-status check. | Needs a new neutral repo lookup (active plugin by canonical type + locale). Follow-up: ut-docs#3211. |
| 3 | minor | After offline consent the tile said "Still installing…", which isn't true when nothing is installing. | **Fixed:** new `queued_offline` note. Test `TestSetupGETResumeAfterOfflineConsentShowsQueuedNote` fails without the fix and passes with it. |
| 4 | minor | "Offline" really means "the catalog fetch failed" (5xx/401 as well), so the copy "because this till is offline" could be untrue. | **Fixed:** the copy now says the plugin catalog couldn't be reached (all locales and help). |
| 5 | minor | A comment overclaimed that the re-resolve "just retried" the catalog (30 s retry interval). | **Fixed** wording. |
| 6 | nit | The setup_page.go comment should say the Offline tile markup is now in the DOM for every till. | **Fixed.** |
| 7 | nit | The de help used an en dash. | **Fixed.** |

## Verification

- TDD: the reviewer reverted the handler/resolver (keeping only the field) and 4 of the new tests failed; restored, all passed. I verified the queued-note test the same way.
- Driven run: I built the binary and ran it with an unreachable marketplace, then used headless Chromium on `/setup` with DE at step 3. The Offline tile rendered with its note and the "Install when online" button at 1024×600 and 360 px, and I looked at the screenshots. Clicking the button returned in about 150 ms to `/setup?tax_country=DE&tax_plugin_pending=1` with the pending note.
- Passed: `go build ./...`, `go vet`, `go test ./...`, and every `ci.yml` build-job guard except `guard-deadcode-baseline` and `guard-shellcheck-version`, which also fail on `main` in this container (environmental).
- Language packs: `validate.sh` and `check-key-drift.sh` pass against this branch's `en.json`.

## Verdict

Safe to merge. Merge core first, then the de/es pack PRs (lang-pack-drift goes red on `main` until they land, as expected).
