# Code review — card-data schema guard (ut-docs#3372, ADR-0127 §7c)

**Date:** 2026-10-02 · **Author:** Opus 5.5 (lane:cloud-24) · **Reviewer:** Fable (independent, different model)

## What shipped
`scripts/ci/guard-card-data-schema.sh` + `_test.sh`, wired into CI (`ci.yml` build job), plus a CLAUDE.md rule.
It fails when the schema names a card number/PAN, BIN/IIN, CVV/CVC/CSC, track/magstripe data, a PIN block or first-6 digits.
ADR-0127 §4 fields (last4, brand, exp_month/exp_year, PSP token/customer ids) pass.
Scans `internal/db/migrations/*.sql` (identifiers after stripping comments, including multi-line `/* */` ones, and string literals). The only existing hit, `payments.masked_pan` in `001_init.sql` (#792, scheme + last 4, enforced by `pos.validateMaskedPAN`), is in `scripts/ci/card-data-schema-allowlist.txt` with its reason, because shipped migrations are frozen (ADR-0100).
How it matches: names are converted camelCase → snake_case and split on `_`. Forbidden words have to match a whole segment, so `binary`, `tracking` and `company` are fine. A forbidden word with digits added (`pan2`, `first6digits`) is caught, and so are phrases such as `card_number` and `pin_block`.
Exception: a same-line comment marker, `card-data:allow <reason>`. The reason is required, and the marker only counts inside a real comment, not inside a string.

## Findings (Fable review)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Till guard had no camelCase handling (`fullPan` passed) | fixed — `snake()` + `camel-*` cases |
| 2 | major | ut-cloud camel test used `cardNumber` (already a word) so a broken `snake()` survived | fixed — `cardPan`/`cardCvv`; mutation re-run now fails |
| 3 | major | Glued-digit variants and `csc`/`cvn`/`magstripe`/`track3` missed | fixed — digit-suffix rule + words, cases added |
| 4 | minor | Marker inside a string literal exempted a line | fixed — strings stripped before the marker check |
| 5 | minor | Greedy `/* */` strip swallowed a column between two comments (till) | fixed — comment walker |
| 6 | minor | Multi-line `/* */` comments were scanned (till) | fixed — tracked across lines |
| 7 | minor | ut-cloud missed `edge.Column`, subdirectory mixins, `field.String( // c` + next-line name | fixed; a Go-constant name stays out of reach (documented) |
| 8 | minor | ut-cloud `internal/repositories/migrate/sql/*.sql` not scanned | accepted — nothing applies them (Postgres auto-migrates from ent); stated in the header |
| 9 | minor | `bin` hits a stock-bin column | accepted — marker with reason; stated in the header + a test case |
| 10 | minor | Allow-list not restricted to frozen files; one marker exempts a whole line | accepted — header says keep one column per line; allow-list entries are reviewed like any diff |
| 11 | nit | `"card number"` quoted identifiers with spaces/dashes | fixed in the till guard (SQL); n/a for ent |
| 12 | nit | Phrase lists drifted between repos | fixed — same lists |

## Verified beyond the automated tests
- Mutation tests in scratch copies: removing `cvv` from the word list, disabling camelCase, breaking the next-line / allow-list path / marker-reason rules each make the self-test fail.
- Run on mawk 1.3.4 (the ubuntu CI awk); shellcheck clean.
- Without the allow-list the real tree fails on exactly the two `masked_pan` lines; with it, all 61 migrations pass.

**Verdict:** safe to merge. Only CI scripts change; no runtime code.
