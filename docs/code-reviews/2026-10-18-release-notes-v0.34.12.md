# Review: release notes v0.34.12

Date: 2026-10-18 · Branch `docs/release-notes-v0.34.12`
Written by the `:41` build lane (`lane:cloud-41`). Reviewed independently by a Sonnet subagent.

## What shipped
Owner-facing "What's new" notes for v0.34.12 in en/de/tr/fa/ar. A release is due: 7 feat/fix merges since v0.34.11.
- New: the Inventory page in my. (Manage shop). It shows stock per location, and a manager can adjust a quantity with a reason. Its my. side has shipped.
- Improved:
  - The gift voucher item created at setup is renamed into the shop's language when that language arrives later.
  - Plugins can answer imports and exports in the operator's language.
  - Lighter sales-report syncing.
- Fixed: the Gift vouchers search ignores case for accented letters, keeps its filter in the address, and hides the empty export.
- Left out on purpose, as groundwork with no owner-visible effect yet:
  - the cloud-master catalogue outbox and its follow-ups;
  - the plugin settings v2 manifest schema.

## Findings (all fixed unless noted)
- Medium, all locales: the sales-report bullet overclaimed ("no longer rebuilds past days that haven't changed"). The fingerprint is salted with the build version and the date, so each day is still rebuilt once a day and after an update. It now says "only when something changed on that day, instead of every time".
- Low, all locales: "every stock-tracked item" became "every active stock-tracked item", which is what the snapshot carries.
- Low, all locales: the plugin bullet's heading now says plugins *can* answer in your language. The change only helps plugins that read the new field.
- Low, ar: the voucher item is now "منتج قسيمة الهدية" and the search heading "البحث في قسائم الهدايا", matching the Arabic help.
- Low, de: "Gutschein-Artikel" is now hyphenated, as in the German help.
- Low, accepted: the rename also skips a shop that has several gift voucher items. That is too fine for a release note; the "renamed it yourself" caveat covers the common case.

## Verified
- my. labels match `ut-cloud/locales/*.json`: `manage_app.title` and `manage_app.node.inventory`.
- Each bullet was checked against its merge diff.
- No page test's string assertion collides with the new text. The reviewer grepped every `Contains`/`Count` literal in `internal/pages/*_test.go`.
- These pass:
  - `scripts/ci/guard-release-notes.sh v0.34.12`
  - `go test ./internal/releasenotes/`
  - `go test ./internal/pages/ -run 'About|Settings|ReleaseNote|Whats'`

## Addendum: #2054 (ut-docs#4650) added

#2054 merged after the notes were written (`577f533`), so this release has 8 feat/fix merges. A new **Fixed** bullet, "Search in more languages", is now in all five locales. The stale-PR sweep (`lane:cloud-41`) added it, and a Sonnet subagent reviewed it independently.

Findings (fixed):
- Low-medium: "customer search" was too broad. The fold covers customer *names* only, not phone or email, and SKU and barcode are not covered. The bullet now says "customer name search" and "these name searches".
- Low, de: "berücksichtigen" understated the change. It now says "behandeln … als denselben Buchstaben".
- Low, accepted: the bullet repeats the voucher bullet's MÜLLER example. It is kept because it is now true for items and customers too.

Verified against `28ab851bd`: the sell-screen search uses `SearchItemsForShortcuts`, which folds via `ut_search_fold`. ISPARTA, Isparta and ısparta all fold to `isparta`. ß folds to ss. NFD input is normalised to NFC. Re-ran `guard-release-notes.sh v0.34.12` and the page tests. Both pass.
