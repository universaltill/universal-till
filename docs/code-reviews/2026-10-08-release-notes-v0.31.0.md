# Review — v0.31.0 release notes (en/de/tr/ar/fa)

- **Date:** 2026-10-08
- **Lane:** lane:local
- **Written by:** Claude Opus 5.5 (subagent)
- **Reviewed by:** Claude Fable 5.1 (independent subagent)

These notes cover the 27 first-parent merges since v0.30.22. The owner-visible changes are:
- the dine-in/takeaway prompt can be switched off;
- a till chooses its role when it joins;
- plugin pages, jobs, storage and views;
- faster plugin start (the persistent compile cache);
- inventory lists every item;
- clear reasons when a setting is refused;
- fewer background calls;
- the shop country is sent at registration;
- plugin LAN permission and reserved pages;
- negative amounts show their sign first;
- the TSE outage override is set on the main till;
- tax plugins see the real time.

**Left out** (internal only): #1755 (benchmark tool), #1754 (retention bookkeeping), #1752 (comment), #1747 (release signing), #1742 and #1736 (tests), and #1759 (cloud snapshot; the my. badge isn't shipped yet).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | must | The plugin start-time claim gave the Pi 4 figure for every Pi. A Pi 5 starts cold in about 3.4 s. | **Fixed** in all five files: now "on a Raspberry Pi 4 … about half a second instead of around ten seconds". |
| 2 | should | The de "save failed" message wasn't the UI string. | **Fixed:** „Speichern fehlgeschlagen“. |
| 3 | should | The fa "save failed" message wasn't the UI string. | **Fixed:** «ذخیره ممکن نشد». |
| 4 | should | The role bullet left out the satellite rule: self-order only, plus a confirmation prompt. | **Fixed** in all five files, using the UI's own prompt labels. |
| 5 | should | The ar notes say «الرئيسي» for "main till"; the UI says «الأساسي». | **Accepted for now.** Every earlier ar release note uses «الرئيسي». Aligning them is a separate translation clean-up. |
| 6–8 | nit | The by-order-type condition, audit views, and the reserved-pages wording. | **Not applied.** Applying them would mean re-translating all five files for little gain. |

The reviewer checked and found correct:
- every bolded label against `web/locales/*.json` and the de pack;
- no PR or card numbers, no competitor names, no compliance claims;
- RTL text and the negative-amount examples;
- `guard-release-notes.sh v0.31.0`.

## Verdict

Safe to merge.
