# 2026-11-04 — code PRs carry English only (ut-docs#5478)

Owner rule (ADR-0160 section 2, "Code merges with English only"): code PRs add
the English source for strings and help; other languages arrive via Weblate and
signed bundles.

## What changed
- `guard-i18n.sh` check 2: ar/fa/tr key-set difference vs `en.json` is a
  non-blocking warning (missing and orphan keys). Kept blocking: every used
  `T`/`TM` key exists in `en.json`, Go-side literals, duplicate keys, the
  `en.meta.json` rules, and verb/ICU parity of keys a locale does define (a
  wrong existing translation is still a bug, a missing one is not).
- Help: `guard-help-topics` (missing translations -> warning via
  `translationWarnings`; route coverage and helpLink ids stay blocking),
  `guard-help-drift` (all translation drift and baseline findings -> warnings),
  `guard-docs-shots` (only the `en` screenshot and topic hash are required, so
  an English change cannot make fa/ar/tr "stale"). Removed the two Go tests that
  demanded fa/ar/tr/de topic completeness
  (`TestManualIsTranslatedInEveryShippedLocale`,
  `TestManualIsFullyTranslatedIntoGerman`).
- `lang-pack-drift.yml` is advisory on push to main too; `locale-render-audit`
  is report-only on push (PRs unchanged: hardcoded English still fails).
- `CLAUDE.md` i18n and help rules reworded.

## Tests
- `guard-i18n_test.sh`: an en.json-only key used by a template passes; the same
  template without the key still fails.
- `checkhelptopics`: `TestEnglishOnlyTopicIsWarningNotFailure`.
- `checkhelpdrift`: the five drift tests now assert warn-only.
- All of `scripts/ci/...`, `internal/pages`, `internal/manual`, and the
  guard shell tests pass locally.

## Not changed, on purpose
- Existing `ar/fa/tr.json` and `web/help/{ar,fa,tr,de}` are NOT deleted: tills
  still embed them as the offline/first-run fallback until bundle delivery fully
  covers them. `ut-translations` untouched.
- Open PRs universal-till#2320 and #2321 add fa/ar/tr/de help files; once this
  merges those translations can be dropped (not edited here).

## Review
Independent pass (different-model subagent), all triaged:
- Fixed: `TestAgentSettingsCatalogue_LinksAndWordsExist_4129` required every
  locale (now en only); wrong "warning" label on real load/write errors in
  help-drift (reverted); lang-pack-drift now downgrades only the script's drift
  verdict (a script/bash error still fails); guard-i18n warning lists keys;
  stale comment and test wording.
- Accepted: `locale-render-audit` `continue-on-error` on push also hides infra
  failures on main; deliberate, PRs stay strict.
- Kept blocking, by design: verb/ICU parity for keys a locale already defines
  (changing an existing key's placeholders in en only still fails until the
  translation follows; use a new key per CLAUDE.md).
- Out of scope, follow-up card: `guard-android-i18n.sh` (native Android
  `values-*/strings.xml` parity) still blocks.
