# Review: Settings → Tills shows the till's role; "primary" becomes "main till" (ut-docs#2782)

Date: 2026-10-16 · Branch `feat/2782-tills-settings-main-till-wording` · lane:cloud-41
Author: Sonnet (Dev subagent, `complexity:easy`) · Reviewer: Opus 5.5, fresh context, in a separate worktree

## What shipped
- **Settings → Tills now starts with this till's role.** It reads "This till: Main till" on a main or standalone till. A joined till shows the Additional till or Satellite label, plus "Main till: <host:port>", taken from `sync.primary_url` with the scheme, path and credentials stripped (`mainTillHostPort`).
- **The register picker is relabelled** "Cash register for payouts" (same key, value only). `registers.strand_warning` and the help topics that quote the label now use it.
  - There is now a link to the Registers page. It appears only when the session holds `stock_location_management`, the same action `/registers` and its nav slot check.
- **"primary" wording** for the main till is now "main till" in 38 `en.json` values (keys and placeholders unchanged), and in tr ("birincil kasa" → "ana kasa").
  - ar and fa already used their main-till term.
  - The help prose in en, tr and de is updated, with headings and structure unchanged.
  - The barcode meaning (`catalog.primary`, `catalog.keypad.set_primary`) is untouched.
- **3 new keys**, in all 4 core locales with `en.meta.json` metadata: `settings.tills.role_label`, `settings.tills.role_main`, `settings.tills.main_till_addr` (%s).
  - The de/es packs get the new keys in their own PRs, plus the new register label. The de pack's last "Primärkasse" (`elevation.summary.sync_promote`) becomes "Hauptkasse".
- **Stray registers** (the card's third point) were already handled: the Registers page renames and deactivates, and the picker lists only active registers. This change adds the missing link to that page.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Help still quoted the old register label: en/tr/ar/fa `multitill.md`, and de `display.md`, `multitill.md`, `reports.md` (de was already stale on main). | Fixed in all five locales. de now quotes the pack's new value. |
| 2 | minor | The Registers link was shown to anyone with `settings`, but `/registers` checks `stock_location_management`, so it could lead to a 403. | Fixed: the link is gated (`CanManageRegisters`). New test `TestSettingsPage_TillsCardRegistersLinkFollowsRegistersPermission` revokes the action and expects no link; it was seen failing without the gate. |
| 3 | minor | ar uses الأساسي ("primary/basic") for the main till. | Accepted. It is ar's established term for the main till (about 77 strings against about 20 for الرئيسي) and does not mean the English "primary" this card is about. Unifying ar is not this card's scope. |
| 4 | minor | `TestTillsPage_ShowsPrimaryRowTaggedAsPrimaryWhenViewingFromAReplica` matched "the main till", which `tills.promote_help` also renders. | Fixed: it now asserts the roster tag `(<tills.the_primary>)`. |
| 5 | minor | No satellite case; `mainTillHostPort` edge cases (no scheme with a path, `user:pw@host`) untested, and the no-scheme fallback could show credentials. | Fixed: added a satellite case and the `TestMainTillHostPort` table. The fallback now strips the path and userinfo. |
| 6 | nit | The ": " after `role_label` is hardcoded in the template. | Accepted (matches nearby label patterns; all 4 locales read correctly). |
| 7 | nit | tr "Ödemeler için kasa" was ambiguous (kasa means both till and register), and `"…kasa"nı` was the wrong suffix. | Fixed: "Ödemeler için nakit kasası" everywhere, which also makes the "nı" suffix correct. |
| 8 | nit | The link has no separator; `SyncPrimaryURL` is read three times per request. | Accepted (trivial). |

## Verified
- **TDD:** `TestSettingsPage_TillsCardShowsRoleAndMainTillAddress` fails on origin/main's handler and template ("want role line \"This till: Main till\"") and passes with the change. The reviewer re-verified this in a separate worktree. The permission test was seen failing with the gate removed.
- **Placeholders:** every changed value in en/tr/ar/fa was diffed against origin/main. No placeholder was added, dropped or reordered, and there are no duplicate or removed keys.
- **Driven run** (Playwright, real till): the Settings → Tills card at 1024×600 in en and fa (RTL), and at 360px in en. I read the screenshots: the role line is on top, the relabelled picker is below it, nothing is clipped, and RTL mirrors correctly. `settings-till-name-taken-3308` and `tills-lan-discovery` pass.
- **Not looked at:** a joined-till render in the browser (the e2e till is always a main till; the Go handler test covers it), dark theme, a physical device.
- **Docs-shots:** CI's first run flagged stale manual screenshots; `make docs-shots` regenerated en/tr multitill (looked at — shows "main till"), guard + its self-test green.
- **Gate:** gofmt, build, vet, `go test ./...`, golangci-lint (0 issues), and the guards: i18n, i18n-meta, help-drift, help-topics, rtl, compliance-claims, competitor-naming, data-access, no-showmodal, no-inline-handlers, mail-addresses, core-neutral, kiosk-engine, page-http-error, readme-local-links.

## Verdict
Safe to merge. The de/es pack PRs follow the core merge, per the new-key rule (#1576).
