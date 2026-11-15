# Review: cloud-terms accept step on till registration (ut-docs#4720, part of #4671)

Scope: `internal/enroll/terms.go` (new), `internal/enroll/enroll.go`
(`register` payload), `internal/pages/enrol_terms.go` (new),
`internal/pages/settings_page.go` (`POST /api/enrol/now`,
`POST /api/settings/auto-register`), `internal/pages/setup_page.go`
(`autoRegisterForSetup`), `web/ui/pages/settings.html`, `setup.html`,
`web/ui/partials/enrol_terms.html`, settings scope
(`internal/data/sync_admin_repo.go`, `internal/db/join_snapshot.go`), locales
en/ar/fa/tr + `en.meta.json`, help `web/help/*/claim.md`, e2e
`e2e/tests/enrol-terms-accept-4720.spec.ts`. Cloud side: ut-cloud#553
(#4717). Built by Sonnet, reviewed by an independent Opus pass; triage and
fixes by the orchestrating session.

## What changed
- `enroll.TermsVersion = "v0.1-draft"` (must be a version ut-cloud's
  `internal/terms` publishes); `TermsURL(locale)` →
  `universaltill.com/<en-gb|de-de|tr-tr>/legal/cloud-terms/` (ADR-0038, page
  from #4670).
- Per-till acceptance in `marketplace.terms_version` / `_locale` /
  `_accepted_at` (never admin-synced, never join-copied). `register()` sends
  `terms_version` + `terms_locale` only when the stored version is current.
- Register now, the auto-register toggle (on an unregistered till) and the
  wizard's register tick need an unticked-by-default accept box with the
  version and the link. Unticked → nothing sent, translated message; checked
  before PIN elevation; the tick survives the elevation round trip.
  Accepted → a muted "Terms accepted: version, date" line.
- Plugin-install registration (`EnsureRegistered`) unchanged: sends an
  acceptance only if one exists (follow-up #4791). An already-registered till
  can't send one afterwards (#4792).

## Review findings and triage
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | MED | Wizard recorded `st.Locale` (country default by then), not the language the terms were shown in. | **Fixed**: `httpx.RequestLocale(r)`; test `TestSetupWizardRecordsTheTermsLanguageShownNotTheCountryDefault` (failed first with `de`). |
| 2 | MED | A future cloud version bump could 400 every till that stored an older acceptance. | **Not a defect today**: ut-cloud's `internal/terms` keeps old versions in its published list by contract (`docs/api-reference.md`). A till build that bumps `TermsVersion` ignores an older stored acceptance (`AcceptedTerms`), so it sends nothing until re-accepted. |
| 3 | LOW | Registered till printed the accepted line twice; auto-register form asked again. | **Fixed**: the auto-register form shows the step only on an unregistered till. |
| 4 | LOW | Re-saving an unchanged opt-in on a registered till was refused. | **Fixed** with 3: the toggle needs the tick only when the till is not registered (itself or via main). Fixing it exposed that the handler recorded an acceptance without a tick on that path — now it records only on a tick, and audits `terms_version` only when accepted. |
| 5 | LOW | A replica records an acceptance it never sends (it registers via its main till). | Accepted: harmless local record; the shop's acceptance is the main till's. |
| 6 | LOW | Acceptance stored even when the registration attempt then fails (offline, unnamed shop). | Accepted: the operator did accept; it is sent on the next attempt. |
| 7 | LOW | Wizard render test didn't pin the Alpine wiring. | **Fixed**: asserts `x-model`, `x-show`, `:required`. |
| 8 | LOW | German strings come from the de pack. | Pack PRs de/es/pt follow the core merge (same cycle). |
| 9 | LOW | "Read the terms" is an inline link, not touch-sized. | Accepted: same idiom as the existing claim link; the whole tick-box label is tappable. |

## Verification
- Unit/handler tests (enroll, pages, data, db) — see the gate below.
- e2e `enrol-terms-accept-4720.spec.ts`: accept step inside the card, no
  horizontal overflow, unticked by default, en-gb link, at 1024×600 and
  360×740, EN and FA (RTL); Register now without the tick shows the message.
- Driven run, looked at: Settings → Till registration (EN 1024, FA 360,
  refused state) and wizard step 7 (EN 1024, FA 360): box hidden until the
  register tick, then required; no JS errors. Not looked at: dark theme,
  German UI (needs the pack).
