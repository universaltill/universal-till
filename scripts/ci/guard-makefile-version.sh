#!/usr/bin/env bash
#
# Regression guard for ut-docs#369: `make build`'s LDFLAGS once stamped
# `-X main.version=...`, a symbol that doesn't exist anywhere in this
# codebase (the app actually reads internal/buildinfo.Version). `go build`
# does not error on an -X target that isn't a real symbol, so that was a
# silent no-op — every `make build` binary reported Version="dev", and
# internal/updates.Newer treats "dev" as older than every release, so with
# auto-update on the till would silently replace a just-built/deployed
# binary with the latest GitHub release minutes later. This guard builds via
# the real Makefile target with a distinctive VERSION and fails if that
# version isn't actually embedded in the binary, or if a real version is
# indistinguishable from the "dev" fallback.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

CHECK_VERSION="0.0.0-guard-makefile-version"
BIN="bin/unitill-pos-guard-check"

cleanup() { rm -f "${BIN}"; }
trap cleanup EXIT

make build VERSION="${CHECK_VERSION}" BIN="unitill-pos-guard-check"

# Capture first, then grep the captured text (not `strings | grep -q`) —
# same reasoning as release.yml's equivalent check: piping straight into
# `grep -q` can make the whole pipeline report failure under `pipefail` on
# some platforms even when grep found its match, because grep exits early.
out="$(strings -a "${BIN}" 2>/dev/null || true)"

if grep -qx "dev" <<< "${out}"; then
  echo "❌ guard-makefile-version: 'make build VERSION=${CHECK_VERSION}' produced a binary that still reports the \"dev\" fallback — the ldflags -X version injection is broken (ut-docs#369)" >&2
  exit 1
fi

if ! grep -qxF "${CHECK_VERSION}" <<< "${out}"; then
  echo "❌ guard-makefile-version: 'make build VERSION=${CHECK_VERSION}' did not embed that version anywhere in the binary — the ldflags -X target symbol is wrong (ut-docs#369)" >&2
  exit 1
fi

echo "✓ guard-makefile-version: make build correctly stamps internal/buildinfo.Version"
