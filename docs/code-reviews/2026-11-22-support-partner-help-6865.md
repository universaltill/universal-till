# Review: support-partner help text when a shop's partner is not available (ut-docs#6865, PR3)

Date: 2026-11-22. Design: `notes-6865/02-architect.md` section 5; ADR-0167
Amendment 2026-11-20 sections 3, 5, 10; ADR-0003.

## What shipped

- `web/help/en/support-partner.md`: new section "If your partner cannot help"
  between "What changes" and "Selling is not affected". It says the Support
  partner screen on my. tells the owner, that Universal Till's own support
  helps instead, that the shop stays connected until the owner disconnects,
  and that nothing changes on the till. It promises no contact (the house
  contact is unset in production until `PARTNER_HOUSE_SUPPORT_JSON` is set).
- `internal/pages/help_page_test.go`: `TestHelpSupportPartnerUnavailableSection`
  (200 and the new text on a registered and an unregistered till) and
  `TestHelpSupportPartnerTextNoBannedWords` (banned-word regex, no contact
  detail).
- de, tr, ar, fa topics unchanged on purpose (owner decision 6): translation
  follows via Weblate (ADR-0160); `guard-help-drift.sh` only warns.
- No Go code, template, `en.json` key, network call or till behaviour change.
  Topic stays `public: false`.

## Dev pre-flight

- TDD: tests written first; red against the original topic, green with the new
  text (evidence in the PR description and `notes-6865/06-dev-pr3-till-help.md`).
- Copy: none of suspended / ended / winding / paused / reason; no invented
  phone, email or URL.
- Offline-first (ADR-0003): static help text, nothing blocks checkout or sync;
  no new cloud-to-till field.

- Fix round (Tester finding 1-2): the banned-word regex had backspace bytes instead of `\b` and could never fail; restored, and the contact check now rejects `://` and `www.`. Mutant proof in the PR.

## Tester

Verdict: PASS WITH FINDINGS (notes-6865/08-tester-pr3.md), on head 33fda17.

- Text equals the Architect's wording; no contact, URL, phone or banned word;
  nothing says checkout or sync is blocked.
- Real drive: till built from the PR tree, topic served 200 on a registered and
  an unregistered till; the section renders between "What changes" and
  "Selling is not affected", no raw markdown; de, tr, ar, fa render in their own
  text without the English section (decision 6). Checked at desktop and phone
  width, and RTL.
- Guards: help-topics, help-drift, help-breadcrumbs, help-terms,
  compliance-claims, competitor-naming and mail-addresses all pass.
- CI green on GitHub runners, including test-heavy for internal/pages. The full
  package times out in WSL even on clean main, so no local full-package green is
  claimed.
- Findings fixed in 8f59fe8: (1) HIGH, the banned-word regex held backspace
  bytes instead of the word boundary and could never fail; (2) MEDIUM, "https://"
  was not caught.
- Mutation results after the fix: suspended, ended, winding down, reason,
  paused, https://, www., an email and tel: all turn the test red; "extended"
  stays green; removing the section or renaming its heading turns the section
  test red.
- Accepted LOW: invented phone number, "contact is shown" promise and
  checkout/sync-blocked wording are semantic and stay a human read. The other
  locales lack the section by owner decision 6.

## Reviewer

Verdict: APPROVE, no blocking findings (notes-6865/12-reviewer-pr3.md), on head
8f59fe8.

- Re-verified personally: text identical to the Architect's and consistent with
  the merged my. copy; 0 control bytes in the touched files; real word boundary
  on the regex line; WSL runs of the Help, Manual and OwnerExcluded tests and
  the manual package pass; go vet clean; guards exit 0; mutants rerun clean; CI
  green on the new head.
- Independent different-model review (Sonnet 5.5): no blocking findings; the
  topic is embedded, the regex is valid, the tests are not vacuous.
- Accepted LOW findings, no fix required: semantic rules not regex-testable;
  de/tr/ar/fa follow via Weblate (ADR-0160).
- Optional banned-word extension (stopp, temporar, permanent) was considered and
  not taken: "stopping" is legitimate wording elsewhere, and the current text
  has none of these.
- Outside this PR: Legal sign-off of the wording, due before the #6277 go-live
  (owner decision 1).
