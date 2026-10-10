# Review — release notes v0.31.7

**Date:** 2026-10-10 · **Lane:** lane:cloudsession (DevOps step of ut-docs#4086) · **Written by:** Opus 5.5 · **Reviewed by:** Sonnet (fresh context)

**What shipped:** `web/release-notes/{en,de,tr,ar,fa}/v0.31.7.md` cover the five merges since v0.31.6:

| PR | Change |
|---|---|
| #1846 | LAN TLS serve |
| #1847 | Takeaway percent text field |
| #1842 | fa/ar client digit shaping |
| #1848 | Users actions wrap |
| #1849 | Invalid stored takeaway override |

The release is due: 5 feat/fix merges on `main` since v0.31.6. The `--first-parent` grep counts 4 because #1849's merge subject is a branch name, not `fix(...)`.

## Findings

| # | Finding | Status |
|---|---|---|
| 1 | Button names didn't match the app strings: "Promote" → "Promote to super admin"; tr `users.set_pin` "PIN ayarla"; ar "تعيين الرمز السري" and "مدير النظام"; fa "تنظیم پین" and "ارتقا به مدیر ارشد". | Fixed |
| 2 | Voucher and split-payment terms didn't match the app: tr "hediye çeki"; ar "التقسيم"; fa "تقسیم پرداخت". | Fixed |
| 3 | fa used the jargon word "ممیز" for the decimal separator. | Fixed: "ویرگول" |
| 4 | The Users bullet said "1024-pixel-wide", but the wrap applies below 1280 px. | Fixed: "narrower screens such as a 10-inch till" |

Nothing was dropped or added between locales, and there are no issue or ADR refs.

## Gate

- `guard-release-notes.sh v0.31.7`: ok
- `go test ./internal/releasenotes/`: ok

#1845 (ut-docs#4028: Tab trapped in the remaining non-modal dialogs) merged while the notes PR was in CI. A Fixed bullet for it was added in all five locales, so the release, which will include it, describes it.

#1852 (ut-docs#2851) then merged as well: photo identify and Ask your till now come only from the AI Assistant plugin (2.0+). It got an Improved bullet in all five locales telling owners to install or update that plugin.

**Verdict:** safe to merge.
