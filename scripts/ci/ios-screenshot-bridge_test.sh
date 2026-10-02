#!/usr/bin/env bash
#
# Wiring guard for the iOS app's bug-report screenshot bridge
# (ut-docs#3355): the web capture path only got the iOS share prompt.
#   - ios/UniversalTill/TillWebView.swift registers ScreenshotBridge as a
#     script-message handler with reply, in the PAGE content world (the
#     panel's own script calls it), and hands it the web view;
#   - ios/UniversalTill/ScreenshotBridge.swift answers only the till
#     origin's main frame, snapshots the web view and replies with a PNG
#     data URL;
#   - web/ui/partials/bugreport_panel.html calls the same handler name.
# The Swift compiles only on ios-ci's macOS runner; the panel's JS branch is
# e2e/tests/ios-screenshot-bridge-3355.spec.ts. Self-tested against broken
# fixtures so each check can actually fail.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

check() {
  local web="$1" bridge="$2" panel="$3" bad=0
  # Code lines only: a commented-out registration must not pass.
  grep -v '^ *//' "$web" | grep -q 'addScriptMessageHandler(' || { echo "bridge not registered on the web view"; bad=1; }
  grep -q 'contentWorld: *\.page' "$web" || { echo "bridge not in the page content world"; bad=1; }
  grep -q 'name: *ScreenshotBridge.name' "$web" || { echo "bridge registered under another name"; bad=1; }
  grep -q '\.webView = webView' "$web" || { echo "bridge never given the web view"; bad=1; }
  grep -q 'WKScriptMessageHandlerWithReply' "$bridge" || { echo "bridge has no reply channel"; bad=1; }
  grep -q 'static let name = "utScreenshot"' "$bridge" || { echo "bridge name is not utScreenshot"; bad=1; }
  grep -q 'frameInfo.isMainFrame' "$bridge" || { echo "bridge answers sub-frames"; bad=1; }
  grep -q 'origin.host == tillOrigin.host' "$bridge" || { echo "bridge does not check the till origin"; bad=1; }
  grep -q 'let tillOrigin = webView.url' "$bridge" || { echo "bridge checks a fixed origin, not the web view's current page"; bad=1; }
  grep -q 'weak var webView' "$bridge" || { echo "bridge holds the web view strongly (retain cycle)"; bad=1; }
  grep -q 'takeSnapshot(with:' "$bridge" || { echo "bridge does not snapshot the web view"; bad=1; }
  grep -q 'data:image/png;base64,' "$bridge" || { echo "bridge does not reply with a PNG data URL"; bad=1; }
  grep -q 'messageHandlers.utScreenshot' "$panel" || { echo "panel does not call utScreenshot"; bad=1; }
  return "$bad"
}

web="$ROOT/ios/UniversalTill/TillWebView.swift"
bridge="$ROOT/ios/UniversalTill/ScreenshotBridge.swift"
panel="$ROOT/web/ui/partials/bugreport_panel.html"
if ! check "$web" "$bridge" "$panel"; then
  echo "FAIL: iOS screenshot bridge wiring"
  exit 1
fi

# Self-test: each broken fixture must fail.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
n=0
for spec in \
  'web|s/addScriptMessageHandler(/addUserScript(/' \
  'web|s|^\( *\)config.userContentController.addScriptMessageHandler(|\1// &|' \
  'bridge|s/let tillOrigin = webView.url/let tillOrigin = fixedURL/' \
  'web|s/contentWorld: .page/contentWorld: .defaultClient/' \
  'web|s/screenshots.webView = webView/_ = webView/' \
  'bridge|s/guard message.frameInfo.isMainFrame,/guard true,/' \
  'bridge|s/origin.host == tillOrigin.host,//' \
  'bridge|s/weak var webView/var webView/' \
  'bridge|s/static let name = "utScreenshot"/static let name = "shot"/' \
  'panel|s/messageHandlers.utScreenshot/messageHandlers.shot/g'; do
  n=$((n + 1))
  which="${spec%%|*}" expr="${spec#*|}"
  cp "$web" "$tmp/web$n"; cp "$bridge" "$tmp/bridge$n"; cp "$panel" "$tmp/panel$n"
  sed -i "$expr" "$tmp/$which$n"
  if check "$tmp/web$n" "$tmp/bridge$n" "$tmp/panel$n" >/dev/null; then
    echo "FAIL: self-test fixture $n ($spec) was not caught"
    exit 1
  fi
done

echo "ios-screenshot-bridge: OK"
