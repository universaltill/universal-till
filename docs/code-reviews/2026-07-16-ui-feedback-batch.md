# Code review — Farshid's UI feedback batch (2026-07-16)

**Branch:** fix/farshid-feedback-0716 · **Reviewer:** self (Claude), recorded per repo rules

## 1. Legacy plugin-data migration (the real bug behind "plugin.faq.menu")

Symptom reported: the menu showed the raw key `plugin.faq.menu`. Root cause
found by reproducing with Farshid's own dev environment: the stable-data-dir
change (93338de) migrated ONLY the database — `paths.Plugins()` moved to
`~/Library/Application Support/UniversalTill/plugins` while every installed
bundle's files stayed in `./data/plugins`. The registry still listed the
plugins, so menu entries rendered, but locales (raw key), wasm modules
(3 × "wasm load … no such file" every boot) and assets were all missing.

Fix: `paths.MigrateLegacyData` (renames `MigrateLegacyDB`) now also copies
each legacy plugin bundle into the stable tree — **per-plugin**, not
all-or-nothing, because affected installs already have a stable dir with only
transient content; skips `cache/tmp/downloads/auth/versions`; copy-only
(legacy stays intact); half-copied bundles are removed. `os.CopyFS` preserves
the execute bit (process-runtime plugins). Verified against Farshid's real
data: 0 wasm errors (was 3), menu shows "Help / FAQ", all bundles present.

## 2. One shared touch header everywhere

`nav.html` had two modes: big ☰ Menu button on the sale screen, small-text
inline links on every other page. Now every page renders the same touch
header (logo · ☰ Menu · sync/session chips); the inline `.nav-links` +
`.nav-lang` markup and CSS are gone (language switching lives on /menu, all
destinations are menu tiles). e2e already navigated via `nav-menu` + tiles.

## 3. Status bar pinned to the bottom

`main.container` now grows (`flex: 1 0 auto`) so the footer can't float
mid-screen on short pages, and `.statusbar` is `position: sticky; bottom: 0`
so it stays visible on long pages. Sale-screen layout unchanged (its own
higher-specificity flex rule still applies).

## 4. Bonus fix: quoted `"Online"` in the status bar

`data-online="{{ T … }}"` — html/template strips `data-` and treats `on*`
attribute names as JS context, JSON-quoting the value (repro'd in a minimal
template; `data-offline` was unaffected). Renamed to `data-conn-online`/
`data-conn-offline`.

## 5. Till-registration visibility (Farshid: "encourage them to register")

- Status bar: amber **✦ Register this till** chip (→ /settings#registration)
  shown only while unregistered — registered tills get a clean bar.
- Settings: new **Till registration** card. Registered → ✅ + store/device
  IDs. Unregistered → explanation + benefits copy + manager-only
  **Register now** button (`POST /api/enrol/now`).
- `enroll` package: `CurrentStatus()` (explicit env config counts as
  registered) and `RegisterNow()` (synchronous single attempt; serialized
  with the background loop via `attemptMu` so a till never fires two
  concurrent registers; loop re-checks state after each wait so a manual
  registration ends it).
- Template funcs `enrolled`/`enrolstore`/`enroldevice` follow the existing
  `updateavailable` pattern (httpx baseFuncs → enroll; no import cycle).
- i18n: 9 new keys in en/tr/fa (guard green, files stay sorted).

## Testing

- `go build ./... && go test ./...` green; i18n + data-access guards green.
- New unit test `TestMigrateLegacyPlugins` (per-plugin copy, exec bit,
  transient dirs skipped, never clobbers existing bundles).
- **Full local Playwright e2e: 15/15** (server needs `UT_DEV_MODE=true` for
  the /v1/install stub specs — same as CI).
- Visual verification (screenshots): menu/settings/sale share one touch
  header; status bar pinned; unregistered till shows chip + card CTA;
  `POST /api/enrol/now` against the LIVE marketplace returned the ✅ message;
  registered till shows IDs and no chip.

## Follow-ups

- The register CTA currently lands on anonymous enrolment (ADR-0013 layer 1);
  when increment 2 (claim via id.universaltill.com) ships, the registered
  card should grow a "Claim this store" step.
- Legacy `./data/backups` history is not migrated (new backups already land
  next to the new DB) — revisit only if someone needs old snapshots.
