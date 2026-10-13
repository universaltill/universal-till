# Review — release notes v0.34.0

Owner-facing notes in en/de/tr/ar/fa, written from the Done cards' close-outs: #4251 gift vouchers page, #4225 gift vouchers from a .bkp import, #4022 registers follow tills, #4260 tracking QR removed until the cloud page exists. #4027 (device platform on register) and #3814 (test harness) are internal and not listed.

Checked: page and menu names match the shipped locale strings (`vouchers.title`, `page.title.registers` in each language). Top-up as a sale (#4252) and balance correction (#4253) are not shipped and not claimed. There are no card or PR numbers in the notes. `guard-release-notes.sh v0.34.0` passes.

Verdict: OK to merge, then dispatch release.yml (bump: minor).
