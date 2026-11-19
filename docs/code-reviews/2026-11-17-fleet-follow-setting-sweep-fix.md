# Review: breadcrumb self-test fixture after the #2739 help step (2026-11-17)

PR universaltill/universal-till#2282 (ut-docs#2739), sweep by lane:cloud-41. [no-subagent: one-file test-fixture fix, found by CI on the merge]

- **Finding (CI `build`):** `guard-help-breadcrumbs_test.sh` planted its mismatch in `web/help/tr/updates.md`. The PR adds English step 5 to `updates.md`, and translated topics now come only from Weblate (ADR-0160 §2), so `tr/updates.md` stops lining up item-for-item. The guard skips unaligned topics, so the plant never fired: "planted mismatch" and "baseline entry" failed.
- **Fix:** plant in `web/help/tr/backups.md`, item 1 (`Ayarlar → Yedekler`, key `settings.backup.title`), which lines up with English today. A comment now says why the fixture topic must stay aligned.
- **Verified:** `bash scripts/ci/guard-help-breadcrumbs_test.sh` passes all 10 checks locally (the old fixture failed 2 of them in CI on the merged head). It runs in the guard sandbox; the real tree is untouched (`git status`). shellcheck is not installed in this container, so CI's shellcheck step is the check.
- **Residual:** the next help change that reshapes `backups.md` in English needs the same move. Picking an aligned topic at run time would remove that; not done here (scope).

Pre-flight: web/help n/a (test fixture only); docs-shots n/a; i18n n/a; review record (this file).
