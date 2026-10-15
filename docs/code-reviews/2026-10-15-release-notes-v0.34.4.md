# Review — release notes v0.34.4

- **Date:** 2026-10-15
- **Scope:** `web/release-notes/{en,de,tr,ar,fa}/v0.34.4.md` (owner-facing "What's new", required by `release.yml`)
- **Written by:** Opus 5.5 (pipeline lane `lane:cloud-41`); **reviewed by:** Sonnet (independent, fresh context)

## What it covers

The first-parent merges since v0.34.3, read from each PR and its card's close-out:

- New: manager gift-voucher balance correction with a reason (ut-docs#4253).
- Fixed: a till registers with the cloud only once the shop has a name; Register now points to the Shop name card, and a tax plugin chosen in setup installs once the name is saved (ut-docs#4272).
- Fixed: a plan lapsing between the sync and the catalogue / order-tracking push is no longer listed as a problem (ut-docs#4313).
- Left out on purpose:
  - ut-docs#4132 (chat plugin-view component) is groundwork, and nothing reaches an owner until a plugin uses it (RELEASING.md step 1, ut-docs#4219).
  - ut-docs#4280 (docs-shots disk pin) is internal tooling.

## Findings

Fixed:

- **(medium) fa/ar "Fixed" headings** didn't match earlier notes. They now read `## رفع اشکال` and `## إصلاحات`, and the fa "New" heading is `## تازه‌ها`, as in v0.34.3.
- **(medium) tr** had an ambiguous "tam kasa kataloğu" ("full catalogue"). The bullet is rewritten.
- **(low) tr** had a mixed dative/locative construction in the voucher bullet. It is split into two clauses.
- **(low) ar/fa** made the Register-now button the subject of "asks you". Both now say that pressing it shows a message.
- **(low) all** said only "catalogue" for the lapse fix. All five now name order tracking too.

Accepted:

- tr prose says "mağaza" but the quoted card label is "Dükkân adı". The quote must match the UI, and the locale itself mixes both words.
- "Ran out just as …" simplifies "between the sync and the push". This is fine for owners.

## Verified

- Every quoted UI label matches its locale file exactly. German labels were checked against the `ut-plugin-language-de` pack.
- `scripts/ci/guard-release-notes.sh v0.34.4` passes.
- The compliance-claims and competitor-naming guards pass.
- `go test ./internal/updates/ ./internal/pages/ -run 'Notes|ReleaseNotes|3091'` passes.
- No PR or card numbers appear in the notes.
