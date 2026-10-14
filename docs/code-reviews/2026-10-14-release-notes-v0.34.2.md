# Review — Release notes v0.34.2

**Date:** 2026-10-14 · **Lane:** lane:cloud-24 · **Written by:** Opus 5.5 · **Reviewed by:** Sonnet (fresh-context subagent).

## What shipped

Owner-facing notes `web/release-notes/{en,de,ar,fa,tr}/v0.34.2.md` for the merges since v0.34.1: revoked plugin permissions survive updates (#4145), a VAT rate's share of a sale discount is capped at its goods (#4277), the recovery screen sits behind the LAN guard (#4268), and its safe-mode link only shows where it works (#4290). Added after review, because #1910 merged meanwhile: no running-out predictions/digest when the shop sells without tracking stock (#4134) — wording taken from that PR's commit message and help diff, terms from `settings.stock_tracking.label` in each locale. Likewise #1916 (#4301): each charge banded on its own so invoice/receipt VAT table/Z report match the signed receipt — from that PR's commit message. #4263 (the settings-API refusal of self-order on an additional till) is left out: it changes no screen an owner uses.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The kiosk bullet claimed the display settings changed; #1912 only closed the generic settings API (the display form already had the confirm) | Fixed — bullet dropped (API-only, nothing an owner sees) |
| 2 | major | VAT bullet overclaimed ("never below zero" — a discount larger than all goods still lands on the top band) and misdescribed the cause | Fixed in all five languages: per-rate share capped at that rate's goods |
| 3 | minor | fa typo "حالت ایمنی" | Fixed |
| 4 | minor | Recovery bullet could name UT_UI_ALLOW_CIDRS | Accepted — v0.34.1 already explains it; "devices your installer has allowed" is enough |
| 5–6 | nit | fa term mix; plugin bullet omits the failed-install undo | Accepted |

Terminology checked against the shipped strings (recovery.*, plugin permissions; German against the de pack). `guard-release-notes.sh v0.34.2` and `guard-compliance-claims.sh` pass.

## Follow-up — #4295 (lane:cloud-54)

#1918 (#4295) merged while this PR waited: on an additional joined till the kiosk-user PIN no longer makes it a kiosk, and a device-profile setting sent from the cloud can't make an additional till a kiosk or a satellite a Register/Back office. Bullet added in all five languages, terms taken from that commit's `multitill`/`self-order` help diff. Reviewed by Sonnet (fresh-context subagent): no major/minor findings; nits accepted — "a setting sent from the cloud" mirrors the help wording; "satellite" is defined on the Multiple tills help page; ar uses صندوق as the multitill help does. `guard-release-notes.sh v0.34.2`, `guard-compliance-claims.sh`, `guard-competitor-naming.sh` pass.

**Verdict:** safe to merge, then dispatch `release.yml` with `bump=patch`.
