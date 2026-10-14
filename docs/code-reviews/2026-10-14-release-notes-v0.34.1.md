# Review — Release notes v0.34.1

**Date:** 2026-10-14 · **Lane:** lane:cloud-41 · **Written by:** Opus 5.5 · **Reviewed by:** Sonnet (fresh-context subagent).

## What shipped

Owner-facing notes `web/release-notes/{en,de,ar,fa,tr}/v0.34.1.md` cover the changes merged since v0.34.0:

- the journal shows gift vouchers;
- the import preview tags gift voucher rows;
- the setup wizard requires a till name;
- percents follow the locale's decimal separator;
- the till UI answers only its own machine;
- the VAT remainder never lands on a zero-goods band.

A test merge and a chore merge are left out because shop owners don't see them.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The loopback-only UI change read as a silent lock-out. The note gave no hint how to let a second device back in, and wrongly singled out the PIN screen. | Fixed. The note now says the change applies to all screens, names the `UT_UI_ALLOW_CIDRS` setting (via installer or support), and says guest pages and till sync keep working. |
| 2 | major | "VAT line" misdescribed the translated "Total incl. VAT" line. | Fixed. Each locale now quotes the shipped label. |
| 3 | minor | The cause of the VAT-cent fix was described inexactly. | Fixed: "a VAT rate with no goods". |
| 4 | minor | The journal bullet left out the voided marker and the vouchers-issued total. | Fixed. |
| 5 | minor | The import bullet implied the tag applies only to backups. | Fixed. It now covers any import. |
| 6–10 | nit | Typography and digit-shape options. | Accepted as they are. |

Terminology was checked against the shipped strings (`nav.journal`, `tills.title`, `catalog.item_type.gift_voucher`). `guard-release-notes.sh v0.34.1` and `guard-compliance-claims.sh` pass.

**Verdict:** safe to merge, then dispatch `release.yml` with `bump=patch`.
