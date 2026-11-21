# 2026-11-21 — docs-shots regeneration after the card-reader PRs (ut-docs#6798)

**Change:** `make docs-shots` re-run on Linux, from `origin/main`. Only
`web/help/img/{ar,en,fa,tr}/plugins-listing.png` changed.
`manifest.json` and every other screenshot, `card-readers` included, came out
byte-identical.

**Authors:** Dev was Sonnet (a subagent). Tester and Reviewer were Opus 5.5
(an independent subagent, fresh context).

## What the run found
- **plugins-listing:** this shot was really stale. Its terms checkbox showed
  the old draft cloud-terms label. It now shows "Merchant Terms (version
  merchant-terms-v1.0)" (`en.json`, `enroll.TermsVersion`), the text from
  universal-till#2632. That PR merged 2026-11-20 03:02Z, after the last
  regeneration (988d531a6, 2026-11-19 23:49Z). The pixel diff covers one 14px
  text line: no layout shift and no font noise. RTL (ar, fa) is intact.
- **card-readers:** #2639–#2643 all merged after 988d531a6 and 5311c9230, yet
  the regenerated shot is byte-identical. So the committed screenshot and
  hashes are current.
- **payments:** the topic has no route and no PNG, so there is nothing to
  capture.

## Findings
- None blocking.
- Note: the pre-installed Chromium is 157, against a pin of 149. The script
  warns about it. It has no effect here: 200+ shots are byte-identical to
  main.
- Follow-up: while `card_readers.phone_pairing` is off, the phone-reader
  sections on /tills render nothing (204). `multitill.png` needs a seeded
  phone reader and a fresh capture once that flag goes on. This is noted on
  ut-docs#6798's close-out and not done here, because it cannot be captured
  with the flag off.

## Verification
- `bash scripts/ci/guard-docs-shots.sh` passes: 38 topics, 13 extra shots,
  surface 4c97ee391e02.
- The reviewer viewed the old and new images for en and ar, and the new
  image for fa.

Pre-flight: help topic n/a (no prose change; screenshots only) · docs-shots updated · reference n/a · README n/a · en.json n/a · review record updated.

**Verdict:** ship.
