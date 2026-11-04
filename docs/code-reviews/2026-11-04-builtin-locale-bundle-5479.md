# Review: built-in languages install their first-party bundle (ut-docs#5479)

Author model Sonnet 5.5; independent reviewer Opus (read-only).

## What shipped
`internal/pages/builtin_locale_bundle.go` (+ test): the plugin update scheduler tick
(main/standalone till only) installs the official `com.universaltill.language-<lang>`
bundle for the shop language when that language is compiled in (ar, en, fa, tr).
`resolveAndInstallBasePluginWith` (listing filter); `i18n.builtin_bundle_checked`
classified per-till. Precedence (bundle newer than the binary beats the built-in file,
omitted keys fall back to the file, then English) already lives in `config/i18n.go`
and `plugins/first_party_locales_test.go`; unchanged.

## Findings
| Sev | Finding | Status |
|---|---|---|
| Med-high | Pending-list spec shows a Settings chip for a language that already works, and a dismissed chip returns | Fixed: no pending list; direct, bounded, online-only install; test asserts the list stays empty |
| Med | Highest-version listing from any publisher could be installed unattended | Fixed: official trust tier only (test of the filter) |
| Med | Uninstalled bundle reinstalled daily | Fixed: marker `<lang>@done` once installed; never auto-reinstalled |
| Med | Replica guard untested | Accepted: guard is one `!isReplica` line beside the existing replica test; tick tests need a sync primary fixture. Follow-up if wanted |
| Low | Unconfirmed bare `ar` may become `ar-IQ` after install (same language, existing #1074 behaviour) | Accepted |
| Low | Loose id prefix match | Fixed: only a 2-4 letter regional subtag |
| Low | Throttle written before queue | Fixed: written after the answer; failures retry next tick |

## Verified
Offline/unpublished/failed: the embedded file keeps rendering (no state depends on the install). pages tests for the touched areas, settings-write guard and per-till scope guard green.
Verdict: safe to merge.
