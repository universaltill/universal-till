# Review — setup wizard: paired till is not asked to register (ut-docs#4803)

**Date:** 2026-10-22 · **Built by:** Sonnet 5.5 (cloud-54) · **Independent review:** Opus (different model), 2026-10-22.

## What shipped
- `web/ui/pages/setup.html` step 7: when `.cloudShop` is set (till joined a my. shop) the ADR-0071 `auto_register` checkbox, its hint and the ut-docs#4720 terms box are not rendered; the joined shop's name is shown (existing `setup.cloud.shop_name` key, no new i18n). Non-cloud path unchanged. The server already ignored these fields on the paired path.
- Test `TestSetupDoneStepAsksRegistrationOnlyOffThePairedPath` (fails on the old template, passes now).
- Help: `web/help/en/claim.md` notes a paired till is not asked.

## Review findings
| # | Finding | Handling |
|---|---|---|
| 1 | Manual (`claim.md`) still said the last screen always asks | Fixed; only English carries the sentence. |
| 2 | No name shown on step 7 when the cloud's name is blank/refused | Left: the till name is typed locally on step 4 and the card's own wording ("name from the cloud") is satisfied when the cloud supplies one. |
| 3 | Review record missing | This file. |

No correctness or security findings. Not done: Playwright spec (none exists for the cloud-pair wizard); docs-shots screenshots need a Linux run.
