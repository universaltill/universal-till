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
  internal/thirdparty/webview_go .github/workflows/release.yml .goreleaser.yaml 2>/dev/null || true)"

if [[ -n "${matches}" ]]; then
  echo "❌ webkit guard: webkit2gtk-4.0 reference found (ADR-0028 requires 4.1)" >&2
  echo "${matches}" >&2
  exit 1
fi

if ! grep -q 'webkit2gtk-4\.1' internal/thirdparty/webview_go/webview.go 2>/dev/null; then
  echo "❌ webkit guard: internal/thirdparty/webview_go/webview.go no longer targets webkit2gtk-4.1" >&2
  exit 1
fi

echo "✓ webkit guard: Linux desktop shell targets webkit2gtk-4.1 (ADR-0028), no 4.0 references"
