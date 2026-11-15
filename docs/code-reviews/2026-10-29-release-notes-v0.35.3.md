# Review: release notes v0.35.3

Date: 2026-10-29 · Docs-only change (web/release-notes/{en,de,ar,fa,tr}/v0.35.3.md)

## Scope check

Notes cover `git log --first-parent v0.35.2..origin/main` (PRs #2203-#2219),
read against each card's close-out comment.

Included: gift voucher top-up on an additional till (#2205, card 4416),
refusing to void a voucher sale on an additional till (#2207), translated
held-top-up reason (#2208), PIN lockout escalation (#2216), step-up PIN
session check (#2217), ICU plural category counts (#2218), help names
my.universaltill.com (#2212, #2213), camera overlay Close reachable (#2204),
iOS secrets excluded from backup (#2214) and restored install drops the cloned
identity (#2219).

Excluded:
- #2203 and #2210 (card 5127, UT-0 / UT-1a): groundwork for moving a till into
  a my.-created store; the card is still open and the feature has not shipped.
  To be announced in the release that ships its last slice.
- #2209: test-only flake fix, nothing the owner sees.

## Checks

- Plain New/Improved/Fixed, no PR or card numbers; guard-release-notes.sh ok.
- Translations done by the cycle model against the v0.35.2 glossary and the
  locale packs (till = Kasse / صندوق / صندوق / kasa; additional till =
  zusätzliche Kasse / صندوق إضافي / صندوق اضافی / ek kasa; gift voucher =
  Gutschein / قسيمة هدية / کارت هدیه / hediye çeki); structure identical
  across the five locales.

## Orchestrator check

Read the English note against the merged PRs. One correction: the iOS restore bullet said the till "signs out of your online account". It actually resets its cloud connection and shows a "connect again" notice, and selling carries on. Rewritten in all five locales. Guard and `internal/releasenotes` tests re-run green.
