# Review: Android string guard warns on missing translations (ut-docs#5480)

Date: 2026-11-12 · Lane: lane:cloud-54 · Complexity: easy · Built by Sonnet, reviewed by Opus 5.5 (fresh context)

## What shipped
- `scripts/ci/guard-android-i18n.sh`: a `values-<locale>/strings.xml` that lacks a key from the base
  `values/strings.xml` is now a **non-blocking warning**, the same as `guard-i18n.sh` treats
  `web/locales/*.json` (ADR-0160 §2, "code merges with English only"; #5478). Android falls back to
  the base string at runtime, so a missing translation is not a defect in an English-only PR.
- Still blocking: orphan keys (a key in a locale file but not in base; Android lint's `ExtraTranslation`
  is believed Fatal, so `lintVitalRelease` in `release.yml` would fail on it), string vs plurals type
  mismatches, and placeholder mismatches (they crash `String.format` at runtime). The orphan message
  now says to delete the stale key from the locale file.
- New sandboxed regression test `scripts/ci/guard-android-i18n_test.sh` (12 cases, via
  `guard-test-sandbox.sh`), wired into `ci.yml`'s `build` job right after the guard.
- ar/fa/tr strings files are untouched.

## Findings (Opus review)
No blocking findings. The review checked exit-code paths, test false-pass risk, sandbox leakage and
temp-dir cleanup, and the ci.yml wiring.
1. Low, fixed: the orphan message gave no remedy. It now names one.
2. Info, accepted: case 3 does not assert that the warning names the key. The other cases' messages
   cover the format.
3. Info, accepted: the 20-key "+N more" cutoff in the warning has no test. It is display only.
4. Info, accepted: the warning is plain output, not a `::warning::` annotation. This matches `guard-i18n.sh`.
5. Info, accepted: the test sets its EXIT trap twice. The behaviour is still correct.

## Verification
- Tester: the new test passes 12/12. With the old guard restored it fails 3 cases (it is not a false
  pass), and the tree was restored byte-identical. Hand-planted copies covered missing key (rc 0 with
  warning), orphan (rc 1), missing plus placeholder mismatch (rc 1, both reported), a `values-v33`
  qualifier (skipped) and no locale directories (rc 0).
- The real guard on the real tree passes (11 keys, ar/fa/tr). `shellcheck scripts/ci/*.sh` is clean.
  `guard-ci-tests-sandboxed.sh` passes.
- `ExtraTranslation` severity is from the reviewer's and architect's memory of the lint source (about
  80% confident). It was not checked online. Keeping orphans blocking is the safe side either way.

Pre-flight: review record updated; help topic n/a (CI guard, no till behaviour); README n/a (no claim
changed); en.json/meta n/a (no key); ADR n/a (applies ADR-0160 §2); follow-up deferred → Android native
strings have no Weblate delivery path yet; already covered by ut-docs#4642 (Android shell native strings), noted there.

Verdict: approve.
