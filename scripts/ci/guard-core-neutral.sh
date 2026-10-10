#!/usr/bin/env bash
#
# Guard (ut-docs#2888; owner rule 2026-09-25, #2848; ADR-0121 §11): core Go
# under internal/, cmd/ and mobile/ never tests or selects by a specific country
# code, vendor name or first-party plugin ID. The detector is
# scripts/ci/coreneutral (go/parser, so comments and string literals are
# handled by the parser) — its doc comment lists exactly what is and isn't
# flagged. This wrapper only resolves the base branch's allow-list for the
# shrink-only check.
#
# Env: CORE_NEUTRAL_BASE (default origin/main) — the ref whose
# scripts/ci/core-neutral-allowlist.txt the current list may not grow
# beyond. Unresolvable ref (local clone without it) or no list on that ref
# yet (the PR that introduces the guard) → the shrink check is skipped with
# a notice; stale-entry and new-offender checks always run.
#
# Pre-check (ut-docs#2851): the AI text engine (camera identify, "Ask your
# till") lives in ut-plugin-integration-ai. Core's internal/ai package must
# not come back, and no Go file may import it (or a subpackage of it) —
# host-side background removal is internal/bgremove.
#
# Args (fixture testing): [repo root] [allow-list file].
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCAN_ROOT="${1:-${ROOT_DIR}}"
ALLOWLIST="${2:-${ROOT_DIR}/scripts/ci/core-neutral-allowlist.txt}"
BASE_REF="${CORE_NEUTRAL_BASE:-origin/main}"
LIST_PATH="scripts/ci/core-neutral-allowlist.txt"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

ai_fail=0
if [ -e "${SCAN_ROOT}/internal/ai" ]; then
  echo "❌ core-neutral guard: internal/ai exists — the AI engine lives in ut-plugin-integration-ai (ut-docs#2851); background removal is internal/bgremove" >&2
  ai_fail=1
fi
ai_import='"github\.com/universaltill/universal-till/internal/ai(/[^"]*)?"'
if grep -rlE --include='*.go' --exclude-dir=.claude --exclude-dir=node_modules --exclude-dir=.git \
    "${ai_import}" "${SCAN_ROOT}" >"${WORK}/ai-importers" 2>/dev/null; then
  echo "❌ core-neutral guard: Go file(s) import internal/ai, removed in ut-docs#2851 (the AI engine lives in ut-plugin-integration-ai):" >&2
  sed "s|^${SCAN_ROOT}/|  |" "${WORK}/ai-importers" >&2
  ai_fail=1
fi
if [ "${ai_fail}" -ne 0 ]; then
  exit 1
fi

base_args=()
if git -C "${SCAN_ROOT}" rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
  if git -C "${SCAN_ROOT}" show "${BASE_REF}:${LIST_PATH}" >"${WORK}/base.txt" 2>/dev/null; then
    base_args=(-base-allowlist "${WORK}/base.txt")
  else
    echo "ℹ core-neutral guard: ${BASE_REF} has no ${LIST_PATH} yet — shrink-only check skipped"
  fi
else
  echo "ℹ core-neutral guard: base ref ${BASE_REF} not available — shrink-only check skipped (CI fetches it)"
fi

cd "${ROOT_DIR}"
go run ./scripts/ci/coreneutral -root "${SCAN_ROOT}" -allowlist "${ALLOWLIST}" ${base_args[@]+"${base_args[@]}"}
