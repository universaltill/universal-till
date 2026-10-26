# macOS Local Network prompt text (ut-docs#5026)

## What shipped
- `packaging/macos/Info.plist`: `NSLocalNetworkUsageDescription` (English
  fallback), `NSBonjourServices` = exactly `_unitill-sync._tcp` and
  `_pdl-datastream._tcp` (the two constants in `internal/discovery`),
  `CFBundleDevelopmentRegion` en, `CFBundleLocalizations`.
- `packaging/macos/Resources/<lang>.lproj/InfoPlist.strings` for en, de, tr,
  ar, fa, es, pt (core locales + the de/es/pt language packs). EN text is
  deliberately narrower than the card's example ("receipt printers and your
  other tills"): discovery does not look for card readers.
- `build-app.sh`: one `cp -R packaging/macos/Resources/.` line after the
  Info.plist step, before codesign (so the seal covers it). Hunk is far from
  PR #2151's codesign changes.
- `packaging/macos_local_network_test.go` (written first, seen red): plist keys,
  Bonjour list == Go constants, localizations == lproj dirs == shipped
  locales, per-locale text (named app, translated, length, RTL start, script),
  plutil -lint on macOS, exact staging line before codesign.

## Review (independent Opus subagent; built, vetted, ran 20 mutations)
| Severity | Finding | Outcome |
|---|---|---|
| Medium | Staging test matched a substring; `cp` without `/.`, wrong dest or `echo cp` passed | Fixed: exact line match; mutation re-checked (fails) |
| Medium | fa text began with Latin "Universal Till": macOS could lay it out LTR | Fixed: starts with a Persian word; test enforces Arabic-script first letter for ar/fa |
| Low | "Still English" check was exact equality | Fixed: no two locales may share text; ar/fa need Arabic letters |
| Low | tr: `dükkân`/`bağlanabilmeniz` | Fixed: `mağaza` (matches tr.json), `bağlayabilmeniz` |
| Low | Stray comment name on `packLocales` | Fixed |
| Low | pack list duplicated from check-lang-pack-drift.sh | Accepted; de/es/pt list is small and the test fails when core locales change |
| Info | CFBundleLocalizations also localises macOS system menus | Accepted, harmless |

Service types verified: nothing else in cmd/, mobile/, internal/ names a Bonjour
type; IPP probe and port sweep are unicast TCP and need no entry (they do
trigger the prompt, which the text covers).

## Not verified
- The real macOS prompt was not triggered; only the built bundle (ad-hoc
  signed, `plutil -lint`, `plutil -p`, `codesign --verify`) was inspected.
- The denied path: macOS uses raw multicast, so a denial is not detected and
  the UI shows the generic "none found". Not invented here; filed as
  ut-docs#5030.
- Translations by the model, not native-speaker reviewed (de/tr should get a
  human check).

## Verdict
Safe to merge once CI is green.
