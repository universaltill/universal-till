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

## Tester

(pending)

## Reviewer

(pending)
