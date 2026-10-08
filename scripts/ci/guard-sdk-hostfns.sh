#!/usr/bin/env bash
#
# Guard: every "ut" host function has a Go guest SDK binding and a reference
# entry, and every SDK binding names a real host function (ADR-0121 F4,
# ut-docs#3951). A host function shipped without its binding is one plugin
# authors can only reach by hand-rolling //go:wasmimport again; a binding
# for a function the host no longer exports fails at instantiation on a till.
#
#   bash scripts/ci/guard-sdk-hostfns.sh [HOST_DIR SDK_DIR [REFERENCE_MD]]
#
# HOST_DIR: Go files whose `.Export("name")` calls register the host module
# (default internal/plugins, _test.go excluded). An Export( whose argument is
# not a string literal on the same line fails the guard — it would be
# invisible to it. SDK_DIR: every non-test Go file's `//go:wasmimport ut
# name` (default sdk/plugin).
# REFERENCE_MD: ut-docs reference/plugin-host-functions.md — from DOCS_DIR
# (CI checks ut-docs out there), else the sibling ../ut-docs. DOCS_DIR set
# but missing the file is a FAILURE; with neither, the reference half skips
# loudly (a bare local run without ut-docs nearby).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

host_dir="${1:-internal/plugins}"
sdk_dir="${2:-sdk/plugin}"
ref="${3:-}"
if [[ -z "${ref}" ]]; then
  if [[ -n "${DOCS_DIR:-}" ]]; then
    ref="${DOCS_DIR}/reference/plugin-host-functions.md"
    if [[ ! -f "${ref}" ]]; then
      echo "❌ sdk-hostfns guard: DOCS_DIR=${DOCS_DIR} has no reference/plugin-host-functions.md" >&2
      exit 1
    fi
  elif [[ -f ../ut-docs/reference/plugin-host-functions.md ]]; then
    ref="../ut-docs/reference/plugin-host-functions.md"
  fi
fi

go_files() { find "$1" -maxdepth 1 -name '*.go' ! -name '*_test.go' -print0; }

opaque="$(go_files "${host_dir}" | xargs -0 grep -Hn '\.Export(' | grep -v '\.Export("[a-z0-9_]*")' || true)"
if [[ -n "${opaque}" ]]; then
  echo "❌ sdk-hostfns guard: Export( without a same-line string literal — this guard cannot read its name:" >&2
  echo "${opaque}" >&2
  exit 1
fi
host_fns="$(go_files "${host_dir}" |
  xargs -0 grep -ho '\.Export("[a-z0-9_]*")' | sed 's/.*("\(.*\)")/\1/' | sort -u || true)"
sdk_fns="$(go_files "${sdk_dir}" | xargs -0 grep -ho '^//go:wasmimport ut [a-z0-9_]*' | awk '{print $3}' | sort -u || true)"

if [[ -z "${host_fns}" ]]; then
  echo "❌ sdk-hostfns guard: found no .Export(\"…\") host functions under ${host_dir}" >&2
  exit 1
fi

indent() { while IFS= read -r line; do printf '   %s\n' "${line}"; done; }

fail=0
missing="$(comm -23 <(echo "${host_fns}") <(echo "${sdk_fns}"))"
extra="$(comm -13 <(echo "${host_fns}") <(echo "${sdk_fns}"))"
if [[ -n "${missing}" ]]; then
  echo "❌ sdk-hostfns guard: host functions with no SDK binding in ${sdk_dir}:" >&2
  indent <<<"${missing}" >&2
  echo "   Add //go:wasmimport + raw shim in raw_wasip1.go, the same shim in fakehost.go, and a typed wrapper (ADR-0121 F4)." >&2
  fail=1
fi
if [[ -n "${extra}" ]]; then
  echo "❌ sdk-hostfns guard: SDK binds functions the host does not export:" >&2
  indent <<<"${extra}" >&2
  fail=1
fi

if [[ -z "${ref}" ]]; then
  echo "⚠️  sdk-hostfns guard: no ut-docs checkout (DOCS_DIR unset, no ../ut-docs) — reference check skipped" >&2
else
  undocumented=""
  while IFS= read -r fn; do
    grep -qwF "${fn}" "${ref}" || undocumented+="${fn}"$'\n'
  done <<<"${host_fns}"
  if [[ -n "${undocumented}" ]]; then
    echo "❌ sdk-hostfns guard: host functions missing from ${ref}:" >&2
    printf '%s' "${undocumented}" | indent >&2
    fail=1
  fi
fi

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "✅ sdk-hostfns guard: $(wc -l <<<"${host_fns}" | tr -d ' ') host functions, all bound in the SDK${ref:+ and documented}"
