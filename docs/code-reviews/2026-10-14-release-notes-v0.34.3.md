# Review — release notes v0.34.3

- **Date:** 2026-10-14
- **Scope:** `web/release-notes/{en,de,tr,ar,fa}/v0.34.3.md` (owner-facing "What's new", required by `release.yml`)
- **Written by:** Opus 5.5 (pipeline lane `lane:cloud-24`); **reviewed by:** Sonnet (independent, fresh context)

## What it covers

The first-parent merges since v0.34.2, read from each PR and its card's close-out:

- New: a lapsed till checks in hourly, so a renewal arrives by itself, and managers get **Check now** (ut-docs#4285).
- Improved: per-processor Android packages (38 MB instead of 150 MB) (ut-docs#1541), and the compiled-in marketplace signing key (ut-docs#4292).
- Fixed: the Android in-app update flow (ut-docs#4312), and a self-order satellite now staying a kiosk on staff sign-in (ut-docs#4307).
- Fixed (added after #1928 merged during CI): printed receipt labels now follow the shop's language (ut-docs#4199); the same five locales, wording checked against the PR and its manual change.
- Left out on purpose:
  - ut-docs#4150 is the till side of my.'s sale detail. It is groundwork, and nothing reaches an owner until #4151/#4152 ship (RELEASING.md step 1, ut-docs#4219).
  - ut-docs#2238 (dead-code removal) and ut-docs#4258 (CI test sandbox) are internal, and so is ut-docs#2248 (an operator-only desktop-shell timing override).

## Findings

All of these were fixed:

- **(medium) Arabic terms didn't match the earlier notes and `ar.json`:**
  - "till" now reads «الصندوق»;
  - "marketplace" now reads «متجر الإضافات»;
  - "satellite" now reads «الصندوق التابع»;
  - one passive verb was ungrammatical.
- **(medium) Persian:**
  - "satellite" now reads «صندوق وابسته»;
  - the button label now matches `fa.json` («همین حالا بررسی کن»).
- **(medium) German terms now match the earlier notes:** "Manager", "Kassiererin/Kassierer" and "Tarif". The closing quote marks now match too.
- **(low-medium) The banner was named "plan ended" in every locale:**
  - The real title is "Paid features are paused", and en/tr/ar/fa now quote it.
  - German describes the banner instead of quoting it, because this repo has no de.json to check the pack's label against.
- **(low-medium) Turkish:**
  - the result text now matches the UI ("Ödendiği son tarih: …");
  - "satellite" now reads «uydu kasa».

## Verified

- `bash scripts/ci/guard-release-notes.sh v0.34.3` → ok.
- `go test ./internal/releasenotes/ ./internal/updates/` → ok. This includes the owner-language test, which refuses card numbers and jargon.
- The `internal/pages` release-notes tests → ok.

**Verdict: safe to merge.**
