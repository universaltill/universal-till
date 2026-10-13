# Review: release notes v0.32.1 (ut-docs#4232)

**Date:** 2026-10-13 · **Lane:** `lane:cloud-41` · **Reviewer:** independent Sonnet subagent (author: Opus 5.5)

**What shipped:** `web/release-notes/{en,de,tr,fa,ar}/v0.32.1.md`. It is a single *Fixed* entry for universal-till#1884: a sale that contains a gift voucher was refused at payment when the German tax plugin signs sales. The entry tells shops to update the plugin to 0.12.0.

**Findings:**
- The fa and ar section headings didn't match the earlier notes. Fixed: `## رفع اشکال` and `## إصلاحات`.
- en, de and tr: no issues. All the translations are faithful.
- No compliance or certification claim (`guard-compliance-claims.sh` clean).

**Verified:** `guard-release-notes.sh v0.32.1` ok, `guard-release-notes_test.sh` ok, `go test ./scripts/release-notes-bundle/` ok.

**Verdict:** safe to merge. After the merge, `release.yml -f bump=patch` cuts v0.32.1.
