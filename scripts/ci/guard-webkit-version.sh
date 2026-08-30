#!/usr/bin/env bash
#
# ADR-0028: the Linux desktop shell must target webkit2gtk-4.1, never the
# abandoned webkit2gtk-4.0 (current Raspberry Pi OS / Debian 13 trixie has
# no installable 4.0 candidate at all -- unitill-desktop fails to exec on
# it). Guards against silently regressing back to 4.0 -- e.g. someone
# re-vendoring internal/thirdparty/webview_go from upstream without
# reapplying the one-line patch, or copy-pasting an old apt/goreleaser
# snippet.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

matches="$(grep -rnE 'webkit2gtk-4\.0' \
  --include='*.go' --include='*.yml' --include='*.yaml' \
  internal/thirdparty/webview_go cmd/unitill-desktop .github/workflows/release.yml .github/workflows/ci.yml .goreleaser.yaml 2>/dev/null || true)"

if [[ -n "${matches}" ]]; then
  echo "❌ webkit guard: webkit2gtk-4.0 reference found (ADR-0028 requires 4.1)" >&2
  echo "${matches}" >&2
  exit 1
fi

if ! grep -q 'webkit2gtk-4\.1' internal/thirdparty/webview_go/webview.go 2>/dev/null; then
  echo "❌ webkit guard: internal/thirdparty/webview_go/webview.go no longer targets webkit2gtk-4.1" >&2
  exit 1
fi

# ut-docs#1233: cmd/unitill-desktop's own cookie-persistence cgo (webkit_linux.go)
# is a second site pinning the WebKit ABI, independent of the vendored
# webview_go patch above -- check it explicitly rather than relying on the
# 4.0-regression grep alone, which would silently accept a *missing* pin
# just as easily as it accepts an unrelated file with no pin at all.
if ! grep -rq 'webkit2gtk-4\.1' cmd/unitill-desktop/webkit_linux.go 2>/dev/null; then
  echo "❌ webkit guard: cmd/unitill-desktop/webkit_linux.go no longer targets webkit2gtk-4.1" >&2
  exit 1
fi

echo "✓ webkit guard: Linux desktop shell targets webkit2gtk-4.1 (ADR-0028), no 4.0 references"
