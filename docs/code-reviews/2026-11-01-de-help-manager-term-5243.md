# Review: German manual says "Manager", like the till's UI (ut-docs#5243)

- Date: 2026-11-01
- Lane: lane:cloud-41 (routine `:41`)
- Card: universaltill/ut-docs#5243 (complexity:easy)
- Author model: Sonnet 5.5; reviewer: Opus 5.5 (fresh context, different model)

## Change

- The German manual (`web/help/de/`) used two words for the manager role.
  - "Führungskraft" appeared 12 times, in `backups.md`, `display.md` (steps 9 and 16), `open-orders.md` and `reports.md`.
  - "Manager" appeared everywhere else.
- The German UI (`ut-plugin-language-de` `locales/de.json`) only ever says "Manager", "Manager-PIN" and "PIN eines Managers". It never says "Führungskraft".
- All 12 occurrences now use the UI's term. Articles and case are adjusted (einen/ein/einem Manager). "PIN einer Führungskraft" now reads "Manager-PIN", or "PIN eines Managers" where the English says "a manager's PIN".
- Only words changed: the same lines and the same list items. German is not a docs-shots locale, so there are no screenshots to regenerate.

**Supersedes the wording choice of #3270.** The review of #3270 (`2026-11-01-de-help-manager-wording-3270.md`) picked "Führungskraft" for display.md step 9 because the surrounding manual used it. That review recorded the UI mismatch as an out-of-scope finding, and that finding became this card. The rule now is the UI's term. The new guard enforces it, so the earlier choice can't come back.

### New guard: `scripts/ci/guard-help-terms.sh`

- **What it does.** It reads `scripts/ci/help-terms.txt`, where each line is `<locale>|<forbidden regex>|<use instead>`. It fails with `file:line` for every match in `web/help/<locale>/**/*.md`.
- **Seed entry:** `de|führungskr(aft|äfte)|Manager`.
- **Reviewed exception:** a same-line `help-term:allow <reason>`.
- **Self-test:** `guard-help-terms_test.sh` runs in a sandbox (`guard-test-sandbox.sh`).
- **CI:** both scripts run in `ci.yml`'s `build` job, right after the breadcrumb guard.

## Findings (independent review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (process) | Reverses #3270's wording without saying so | Fixed: the "Supersedes" note above |
| 2 | major (process) | No review record | Fixed: this file |
| 3 | minor | `\|\| true` hid grep exit 2, so a broken regex silently disabled its rule | Fixed: exit ≥2 fails, with a self-test case |
| 4 | minor | Mistyped locale (`DE`) skipped silently | Fixed: a missing `web/help/<loc>` fails, with a self-test case |
| 5 | nit | CRLF left a `\r` in the suggestion text | Fixed: CR stripped |
| 6 | nit | Decomposed umlauts (u + U+0308) don't match | Accepted: none exist today; noted in the guard's header |
| 7 | nit | Allow marker was also matched against the file path | Fixed: matched against the text only |
| 8 | nit | "nach der Manager-PIN" vs English "a manager's PIN" | Fixed: "nach der PIN eines Managers" (the UI's own phrasing) in display.md 9 and open-orders.md 5 |

The full `build`-job guard run also caught one problem in the new guard: `guard-pipefail-grep-q` flagged `grep -o … | head -n 1` (SIGPIPE under pipefail, ut-docs#2946). Fixed by using `sed -n 1p`.

**Out of scope, for a follow-up card:**
- "Kassenkraft" in `display.md` and `printing.md`, where the UI says "Kassierer". The fix would be another `help-terms.txt` entry.
- Lowercase "manager-PIN-geschützte" in `display.md` and `users.md`.

## Verified

- **Guard catches the old text:** reverting `web/help/de` to `main` makes the guard fail, with 10 lines and 12 matches, each with `file:line`. Restoring the change makes it pass.
- **Self-test:** 6/6 cases pass (clean tree, planted term, allow marker, unlisted locale, bad regex, unknown locale).
- **Other guards:** `guard-help-drift`, `guard-help-topics`, `guard-help-breadcrumbs`, `guard-ci-tests-sandboxed`, `guard-compliance-claims` and `guard-competitor-naming` pass, as does every guard in the `build` job.
- **shellcheck:** `scripts/ci/*.sh` has 0 issues.
- **German:** the reviewer checked grammar and case in every changed sentence.

**Safe to merge: yes.**
