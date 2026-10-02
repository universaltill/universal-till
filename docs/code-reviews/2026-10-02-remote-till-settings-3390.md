# Review — the remaining till Settings from my. (ut-docs#3390)

- **Date:** 2026-10-02 · **Lane:** lane:local
- **Repos:** universal-till, ut-cloud, ut-my-shop — branch `feat/3390-remote-till-settings` in each
- **Built by:** Claude Opus 5.5 (Dev subagents) · **Reviewed by:** Claude Fable 5.1 (independent subagent)
- **Owner ask (2026-10-02):** "not all settings are available". Per-key decision table on the card.

## What shipped
- **Editable from my.** (each remote case mirrors its local Settings form's
  validation and side effects, fail closed, shop-wide → refused on an
  additional till): barcode symbologies, import-barcode default, pre-pack
  unit price, allow negative stock, invoice seller name/address/VAT no.,
  idle lock (0–480), kiosk payment mode, shop language (must be installed;
  same side effects as the local Language card), staff languages.
- **Read-only** (refused at till, cloud allow-list and my. endpoint):
  report retention (shortening deletes records), currency, shop type.
- Till reports the effective values (defaults when unset) and installed
  languages (`till_setting_options`); cloud descriptors gain toggle/multi
  kinds, `ReadOnly`, per-key `MinTillVersion` 0.30.16 (→ `409
  till_too_old` before queueing).
- my. Till settings grouped like the till (Sale screen, Receipts, Barcodes
  & labels, Stock, Invoices, Security, Kiosk, Language, Shop); checkbox
  groups as fieldsets; read-only rows say "Change this on the till itself";
  parity.md rows flipped; 55 keys in all 10 locales.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | **major** | A malformed `till_setting_options` (or `till_settings`) failed the whole check-in → the till would never receive directives | **Fixed**: lenient per-entry decode, bad entries dropped; 6 tests (red first). Other strict report sections → card ut-docs#3489 |
| 2 | medium | No audit row for a remote setting change (idle lock and shop language now remote) | **Fixed**: `cloud_till_setting_set` audit row with old/new, via cloud; refused writes none — test red first |
| 3 | low | Any whitelist mismatch reads as `till_too_old` | Accepted (fail-closed) |
| 4 | low | Prerelease `0.30.16-rc1` < 0.30.16 | Accepted |
| 5 | low | A newer till's unknown barcode id makes the set uneditable until the cloud list is updated | Accepted (fail-closed) |
| T1 | low | (Tester) Multi-choice rows centred their label beside a tall list | **Fixed** (`ts__row--tall`, label at top) |

## Verified beyond unit tests
- Reviewer re-verified TDD in separate worktrees: idle-lock bound / locale
  check removed → refusal test fails; read-only refusals removed → test fails.
- Driven run (demo build): grouped settings at 1440×900 en-GB, fa-IR and
  390×844 de-DE, no horizontal scroll; screenshots looked at. The demo
  snapshot lacked the new labels (raw key shown) — snapshot re-synced from
  ut-cloud before merge. Not looked at: dark theme, a real till applying a
  remote language change.
- Gates: till vet + pages/cloudsync tests + data-access guard; cloud vet +
  claims/api/httpapi/tests; my. `npm run check` + till-parity guard.

## Verdict
Safe to merge after fix 1 (done): universal-till and ut-cloud first, then
my. A till release (0.30.16) is needed before the new keys apply.
