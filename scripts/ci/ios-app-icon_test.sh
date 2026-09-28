#!/usr/bin/env bash
#
# Regression test for the iOS/iPadOS app icon (ut-docs#3069). App Store
# Connect rejects an upload whose 1024x1024 marketing icon is missing, the
# wrong size or has an alpha channel, and it only says so after a full
# signed archive + upload. This runs ios/scripts/make-app-icon.sh into a
# scratch directory and checks what it wrote:
#   - Assets.xcassets/Contents.json and AppIcon.appiconset/Contents.json;
#   - one universal iOS 1024x1024 entry naming the PNG;
#   - the PNG is 1024x1024, RGB, with NO alpha channel;
#   - the PNG carries artwork (not a blank tile from a failed rasterize).
# And that ios/project.yml wires the catalog in (AppIcon) and the output
# stays git-ignored.
#
# Needs macOS (qlmanage/sips). Elsewhere the rasterize part skips, unless
# UT_REQUIRE_IOS_ICON_TOOLS=1 (set by ios-ci.yml) makes a missing tool fail.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
GEN="ios/scripts/make-app-icon.sh"

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

# --- wiring (any OS) --------------------------------------------------------
if grep -qE '^ +ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon$' ios/project.yml; then
  pass "project.yml sets ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon"
else
  fail "ios/project.yml does not set ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon"
fi
if grep -qxF '/UniversalTill/Assets.xcassets/' ios/.gitignore; then
  pass "generated Assets.xcassets is git-ignored"
else
  fail "ios/.gitignore does not ignore /UniversalTill/Assets.xcassets/"
fi
if grep -qF "$GEN" .github/workflows/ios-ci.yml && grep -qF "$GEN" .github/workflows/ios-testflight.yml; then
  pass "ios-ci.yml and ios-testflight.yml both run ${GEN}"
else
  fail "${GEN} is not run by both ios-ci.yml and ios-testflight.yml"
fi
if grep -qF 'scripts/ci/ios-app-icon_test.sh' .github/workflows/ios-ci.yml; then
  pass "ios-ci.yml runs this test"
else
  fail "ios-ci.yml does not run scripts/ci/ios-app-icon_test.sh"
fi

# --- generate + inspect (macOS) ---------------------------------------------
if ! command -v qlmanage >/dev/null 2>&1 || ! command -v sips >/dev/null 2>&1; then
  if [ "${UT_REQUIRE_IOS_ICON_TOOLS:-}" = 1 ]; then
    fail "qlmanage/sips not found (UT_REQUIRE_IOS_ICON_TOOLS=1)"
  else
    echo "skip - qlmanage/sips not available (not macOS): icon rasterize not checked"
  fi
else
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  out="${work}/Assets.xcassets"
  if [ ! -x "$GEN" ]; then
    fail "${GEN} is missing or not executable"
  elif ! bash "$GEN" "$out" >"${work}/gen.log" 2>&1; then
    cat "${work}/gen.log" >&2
    fail "${GEN} exited non-zero"
  else
    pass "${GEN} ran"
    set_dir="${out}/AppIcon.appiconset"
    png="${set_dir}/AppIcon-1024.png"
    if [ -s "${out}/Contents.json" ] && [ -s "${set_dir}/Contents.json" ]; then
      pass "catalog and appiconset Contents.json written"
    else
      fail "missing Contents.json under ${out}"
    fi
    compact="$(tr -d ' \n\t' <"${set_dir}/Contents.json" 2>/dev/null || true)"
    for want in '"filename":"AppIcon-1024.png"' '"idiom":"universal"' '"platform":"ios"' '"size":"1024x1024"'; do
      if grep -qF -- "$want" <<<"$compact"; then
        pass "appiconset Contents.json has ${want}"
      else
        fail "appiconset Contents.json lacks ${want}"
      fi
    done
    if [ ! -s "$png" ]; then
      fail "no ${png##*/} written"
    else
      props="$(sips -g pixelWidth -g pixelHeight -g hasAlpha -g samplesPerPixel "$png")"
      for want in 'pixelWidth: 1024' 'pixelHeight: 1024' 'hasAlpha: no' 'samplesPerPixel: 3'; do
        if grep -qF -- "$want" <<<"$props"; then
          pass "icon PNG ${want}"
        else
          fail "icon PNG is not '${want}' (App Store needs 1024x1024 opaque RGB):"$'\n'"${props}"
        fi
      done
      # Through the same qlmanage -> JPEG -> PNG path a blank white 1024 tile
      # comes out at ~16 KB and the real icon at ~43 KB (measured 2026-09-28).
      # Catches a rasterize that silently drew only the tile.
      bytes="$(wc -c <"$png" | tr -d ' ')"
      if [ "$bytes" -gt 30000 ]; then
        pass "icon PNG carries artwork (${bytes} bytes)"
      else
        fail "icon PNG is only ${bytes} bytes — looks blank (did the SVG rasterize?)"
      fi
    fi
  fi
fi

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all iOS app icon checks passed"
