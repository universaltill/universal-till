#!/usr/bin/env bash
#
# Regression test for ut-docs#6654: web/help/img/manifest.json records the
# app surface PER FILE (surface_files), so two PRs that touch different
# surface files merge without a textual conflict. Before, one global
# surface_sha256 line was rewritten by almost every UI PR and conflicted by
# construction.
#
# Runs real `git merge-tree` / `git merge` in a sandbox repo (the sandbox is
# a git repo; guard-test-sandbox.sh). Cases, each branched from one base:
#   1. A edits surface file X, B edits the ADJACENT file Y (updater path)
#   2. same, but B regenerates through lib.js buildManifest (writer path)
#   3. A edits a surface file, B edits a routed topic's markdown (+ writer)
#   4. negative control: both edit the SAME file differently -> must conflict
#      (proves this test can see a conflict, so the passes above are real)
# After each clean merge the guard must pass on the merged tree.
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox web/ui web/public web/help internal/pages e2e/tests-docs

GUARD="scripts/ci/guard-docs-shots.sh"
UPDATER="scripts/ci/update-docs-shots-surface-hash.sh"
FAIL_COUNT=0
OUT="$(mktemp)"
trap 'rm -f "${OUT}"; remove_guard_test_sandbox' EXIT

GIT=(git -c user.name=t -c user.email=t@example.invalid)
"${GIT[@]}" commit -qm base
BASE="$(git rev-parse HEAD)"
git branch -q base "${BASE}"

# The first two ADJACENT entries of the sorted surface under web/public/.
SURFACE_LIST="$(GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1 bash "${GUARD}")"
mapfile -t PUB < <(awk '$2 ~ /^web\/public\// && n < 2 { print $2; n++ }' <<<"${SURFACE_LIST}")
X="${PUB[0]}"
Y="${PUB[1]}"
if [[ -z "${X}" || -z "${Y}" ]]; then
  echo "❌ FAIL: could not pick two adjacent web/public surface files" >&2
  exit 1
fi
# No `| head -1` under pipefail: it can SIGPIPE grep and abort with 141.
mapfile -t ROUTED_MD < <(grep -l '^routes:' web/help/en/*.md)
TOPIC_MD="${ROUTED_MD[0]:-}"

# branch <name> <file> <text> <updater|writer> : edit, refresh, commit.
branch() {
  local name="$1" file="$2" text="$3" how="$4"
  git checkout -q -B "${name}" base
  printf '\n%s\n' "${text}" >>"${file}"
  case "${how}" in
    updater) bash "${UPDATER}" >/dev/null 2>&1 ;;
    writer) node e2e/tests-docs/write-manifest.js >/dev/null ;;
  esac
  git add -A
  "${GIT[@]}" commit -qm "${name}"
}

# merges_clean <label> <A> <B>
merges_clean() {
  local label="$1" a="$2" b="$3"
  if ! git merge-tree --write-tree "${a}" "${b}" >"${OUT}" 2>&1; then
    echo "❌ FAIL: ${label}: merge-tree reports a conflict:" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
    return
  fi
  git checkout -q "${a}"
  if ! "${GIT[@]}" merge -q --no-edit "${b}" >"${OUT}" 2>&1; then
    echo "❌ FAIL: ${label}: git merge conflicted:" >&2
    cat "${OUT}" >&2
    "${GIT[@]}" merge --abort 2>/dev/null || true
    FAIL_COUNT=$((FAIL_COUNT + 1))
    return
  fi
  if bash "${GUARD}" >"${OUT}" 2>&1; then
    echo "✓ ${label}: merges clean and the guard passes on the merged tree"
  else
    echo "❌ FAIL: ${label}: merged cleanly but the guard fails:" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

branch a1 "${X}" "/* zz a */" updater
branch b1 "${Y}" "/* zz b */" updater
merges_clean "adjacent surface files (updater)" a1 b1

branch a2 "${X}" "/* zz a */" writer
branch b2 "${Y}" "/* zz b */" writer
merges_clean "adjacent surface files (writer)" a2 b2

branch a3 "${X}" "/* zz a */" updater
branch b3 "${TOPIC_MD}" "<!-- zz topic edit -->" writer
merges_clean "surface file vs topic markdown" a3 b3

branch a4 "${X}" "/* zz one */" updater
branch b4 "${X}" "/* zz two */" updater
if git merge-tree --write-tree a4 b4 >"${OUT}" 2>&1; then
  echo "❌ FAIL: negative control: two different edits of the same file merged clean (test cannot see conflicts)" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ negative control: two edits of the same file do conflict"
fi

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ all docs-shots manifest merge cases passed"
