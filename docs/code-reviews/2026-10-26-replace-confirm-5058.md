# Review: join-screen "Replace <till>?" confirmation (ut-docs#5058, UT-1)

- **Date**: 2026-10-26
- **Branch**: `feat/5058-ut1-replace-confirm`
- **Design**: ut-docs `reference/design-5058-replace-main-till.md` §3.3 T1,
  §4.2, §4.3, §11; ADR-0166 amendment 2026-10-26.
- **Contract**: ut-cloud `docs/api-reference.md` "Replace codes",
  `internal/paircontract` (CL-1 merged; CL-2 confirm handling not yet).
- **Reviewer**: independent read-only subagent on a different model (Fable).

## What shipped

- `enroll.PairFirstTill` takes a `replaceDeviceID` (sent only with
  `first_till`, at most 200 characters) and returns two typed errors:
  `ReplaceConfirmError{DeviceID, Name, LastUsedAt, Store}` for
  `409 replace_confirm_required` and `MainTillOnlineError{LastSeen}` for
  `409 main_till_online`. A confirmed answer's `data.replaced` is kept on
  `FirstTillPairing.Replaced`. Cloud text is clamped.
- `POST /api/setup/cloud-pair` (same rate gate, nonce, Host and Origin
  guards, unchanged order) renders the confirm panel
  `web/ui/partials/setup_cloud_replace.html` in place: old name, last seen
  (UTC, with the time), the loss sentence, what comes from the cloud
  (cloud-master) or that sample products are used (till-master), and that
  sales history, customers and PINs come back only from a backup file of the
  old till. Nothing is held. **Replace** submits the same form with
  `confirm_replace=1` (only that button carries it) and the hidden
  `replace_device_id`; a stale hidden field without it is never sent.
- A cloud that ignores the confirmation (before CL-2) answers
  `replace_confirm_required` again: the till shows
  `setup.cloud.error.replace_unsupported` instead of looping on the panel.
- In-place messages for `main_till_online`, `replace_target_changed`,
  `replace_needs_owner`, `replace_code_main_till_only` (also on Settings →
  Pair with a shop) and the new `store_has_till` text (new key
  `setup.cloud.error.store_has_main_till`, the old key removed: its
  placeholders changed).
- "Your shop" after a replace shows "Replaced: <name>" and does not offer
  "This is not my shop" (ADR-0166 §4 amendment).
- Help: `web/help/{en,de,ar,fa,tr}/multitill.md` step 5.

## Findings (independent review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Major | `hx-disabled-elt="find button[type=submit]"` matches one element in htmx 1.9.12, so the panel's Replace button stayed enabled during its request (double tap → two confirms). | Fixed: the form has an id and disables `#setup-cloud-form button`. |
| 2 | Minor | `main_till_online` advised making "this till" the main till on my., impossible for an unpaired device. | Fixed wording (another running till can be made main). en/ar/fa/tr. |
| 3 | Minor | `replace_unsupported` invited retries that spend the per-code bucket. | Fixed wording: the manual way (remove on my., pair with an Add a till code). |
| 4 | Minor | Replaced note on "Your shop" not bidi-isolated. | Fixed. |
| 5 | Nit | Expected `replace_confirm_required` Warn-logged as a failure. | Fixed: not logged. |
| 6 | Nit | Variadic optional parameter on `postPairFull`. | Fixed: plain string. |
| 7 | Nit | U+2011 may be missing from kiosk fonts. | Fixed: word joiners (U+2060) round the hyphens, NBSP for spaces. |
| 8 | Nit | Design deviations. | Recorded below. |
| 9 | Nit | `replace_confirm_required` with no usable `device_id` falls to "Pairing didn't work". | Accepted: fails closed; the cloud always sends it. |

Reviewer verified: htmx 1.9.12 sends the clicked submitter's name/value;
stale field handling; loop guard; error-code coverage against the contract;
escaping and clamps; nonce/Host/Origin order unchanged; placeholders in all
four locales; guards (i18n, i18n-meta, RTL, no-showmodal, help drift/topics,
compliance, competitor naming); no file writes.

## Design deviations

- T1 "prefill the till name with the old till's name when empty": not done.
  The name is required before the first request, so it is never empty there.
- §4.5 "dir=ltr spans": done with Unicode isolates (FSI/PDI), because the
  in-place messages are escaped plain text.

## Verified beyond automated tests

TDD: the handler tests were run against `origin/main`'s handler and template
first (7 failed), then pass with the change.

Driven run: a throwaway till (temp `UT_DATA_DIR`, port 8196) against a stub
cloud implementing the replace contract (port 8197), Playwright Chromium:

- 1024×600 (en, ar, de) and 360px (en, fa, ar): panel within the card, no
  horizontal scroll (`scrollWidth` = viewport), buttons 48px (Replace wraps
  to two lines at 360px in en), focus lands on the panel heading (no
  keyboard). Screenshots read: no clipping or overlap; RTL border and
  button order mirror; date stays on one line in ar after the fix.
- Cancel clears the panel; Pair again shows it again; Replace finishes on
  "Your shop" with "Replaced: Front counter till" and no "This is not my
  shop".

Not looked at: dark theme (the setup wizard has no dark theme; it ignores
`prefers-color-scheme`), German text (the `de` strings come from the language
pack, not installed on the throwaway till, so `de` rendered English),
real touch hardware (Android tablet, Pi), the on-screen keyboard. No
end-to-end run against a real cloud: CL-2 is not merged.

## Gate

`go build ./...`, `go vet`, `gofmt`, `go test ./internal/pages
./internal/enroll ./internal/httpx ./web/...`, every guard in `ci.yml`'s build
job (local failures only `guard-shellcheck-version` — local shellcheck 0.11
— and `guard-store-private` — a local awk locale error, also failing on
`origin/main`). `golangci-lint` not run locally (local binary built with an
older Go); CI runs it.

## Verdict

Safe to merge **once ut-cloud CL-2 (`replace_device_id` on `/pair`) is
merged and deployed**: before that, Replace always ends in the
`replace_unsupported` message (harmless, but the flow cannot finish).

## Deferred

- Cross-repo e2e (AC11) once CL-2 is live on the e2e cloud.
- New `en.json` keys need the language-pack PRs (orchestrator).

## Rebase + contract verification (2026-10-26, before merge)

Rebased onto `origin/main` (no conflicts) and checked against the merged,
deployed ut-cloud contract (`docs/api-reference.md`, PRs #680, #684-#686):

- `409 replace_confirm_required` (`data.existing_till{device_id,name,
  last_used_at}` + `data.store`), the confirm request with
  `replace_device_id`, `200` with `data.replaced`, `409 main_till_online`
  (`data.last_seen`), `replace_target_changed`, `replace_needs_owner`,
  `replace_code_main_till_only`: all match what this PR parses and shows.
- The wizard sends `replace_device_id` only on the confirm submit and only
  with `first_till`; the Additional-till and Satellite paths never send it.
- A replay of a confirmed replace answers `403 invalid_code`: the existing
  message already tells the owner to make a new code under Tills, Add a till.
- A reused `device_id` (`409 device_revoked`) cannot happen: every wizard run
  sends a fresh id.
- No mismatch found, no code change. `replace_unsupported` ("cannot replace a
  till yet") now guards only a cloud that ignores the confirmation; the live
  cloud never does. Left as the loop guard.
- Gate re-run: build, vet, `go test` pages/enroll/httpx/web, data-access,
  i18n, i18n-meta guards green; docs-shots regenerated on Linux (only the
  manifest changed; no screenshot of a changed topic differed).
