# Review: deposit-refund dialog keyboard reachability

Date: 2026-08-03  
Scope: `universaltill/ut-docs#288` / deposit-refund action label and dialog interaction

## Changes reviewed

- Replaced the native `showModal()` opener with `show()` so the custom keyboard,
  which is appended to `body`, remains interactive while the dialog is open.
- Positioned `#pfand-modal` consistently with the existing keyboard-compatible
  hold dialog.
- Localized the action and dialog title in English, Arabic, Farsi, and Turkish.
- Added a server-render regression test that guards the opener mode and English
  translation.

## Independent findings

The reviewer found no defects in the frontend or localization changes. The
existing `AuthorizeManager` gate is unchanged and intentionally accepts the
product's elevated `manager` or `admin` roles; it remains the audit actor for
the payout. The issue's approval comment says “admin role”; this implementation
interprets that as the existing manager/admin approval gate rather than
introducing a new authorization policy.

## Verification

- Focused regression test passed (including a pre-fix failure against
  `showModal()`).
- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `scripts/ci/guard-data-access.sh`
- `scripts/ci/guard-i18n.sh`
- `scripts/ci/guard-emoji-font.sh`
- `git diff --check`

Playwright dependencies were not installed in the isolated worktree, so the
browser suite could not be launched locally; the server-render regression test
covers the interaction regression at source level.
