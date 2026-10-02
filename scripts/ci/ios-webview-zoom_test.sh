#!/usr/bin/env bash
#
# Wiring guard for the iOS app shell's zoom lock (ut-docs#3350): tapping a
# text field zoomed the till in, and every page could be pinch-zoomed.
# ios/UniversalTill/TillWebView.swift must:
#   - add the lock script to the WKWebView's userContentController, main
#     frame only, at document end (the viewport meta exists by then);
#   - set the viewport to maximum-scale=1 and user-scalable=no;
#   - never touch ignoresViewportScaleLimits in code (true would let
#     WebKit ignore those limits, as Safari does).
# The web side (fields >= 16px at the phone tier) is
# e2e/tests/phone-input-no-zoom-3350.spec.ts. Self-tested against broken
# fixtures so each check can actually fail.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

check() {
  local f="$1" bad=0
  grep -q 'userContentController.addUserScript' "$f" || { echo "no user script added to the web view"; bad=1; }
  # Separate greps, so a reflow of the WKUserScript(...) call still passes.
  grep -q 'source: *Self.lockZoomScript' "$f" || { echo "lock script not the user script's source"; bad=1; }
  grep -q 'injectionTime: *.atDocumentEnd' "$f" || { echo "lock script not injected at document end"; bad=1; }
  grep -q 'forMainFrameOnly: *true' "$f" || { echo "lock script not main frame only"; bad=1; }
  grep -q 'maximum-scale=1, user-scalable=no' "$f" || { echo "viewport not locked to scale 1"; bad=1; }
  # Any code use (comments aside) could switch the limits off at runtime.
  if awk '!/^ *\/\// && /ignoresViewportScaleLimits/ { hit = 1 } END { exit !hit }' "$f"; then
    echo "ignoresViewportScaleLimits set in code would undo the lock"; bad=1
  fi
  return "$bad"
}

real="$ROOT/ios/UniversalTill/TillWebView.swift"
if ! check "$real"; then
  echo "FAIL: $real"
  exit 1
fi

# Self-test: each broken fixture must fail.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
n=0
for broken in \
  's/userContentController.addUserScript/userContentController.removeAllUserScripts/' \
  's/forMainFrameOnly: true/forMainFrameOnly: false/' \
  's/injectionTime: .atDocumentEnd/injectionTime: .atDocumentStart/' \
  's/maximum-scale=1, user-scalable=no/maximum-scale=5/' \
  's/let webView = WKWebView(frame: .zero, configuration: config)/&; webView.configuration.ignoresViewportScaleLimits = flag/'; do
  n=$((n + 1))
  sed "$broken" "$real" >"$tmp/f$n.swift"
  if check "$tmp/f$n.swift" >/dev/null; then
    echo "FAIL: self-test fixture $n ($broken) was not caught"
    exit 1
  fi
done

echo "ios-webview-zoom: OK"
