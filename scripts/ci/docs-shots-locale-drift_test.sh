#!/usr/bin/env bash
#
# Regression test for scripts/ci/docs-shots-locale-drift.sh and the wiring of
# .github/workflows/docs-shots-locale-drift.yml (ut-docs#4582).
#
# collect: runs in a sandbox copy of web/help/img (it reads `git status`
# there), plants a changed, a new and a deleted screenshot, and checks the
# stale list, the copied files, the issue body and the printed count — plus
# a clean tree printing count=0 and writing no body.
# sync-issue: a stub `gh` on PATH logs every call; checks the four cases
# (drift + no issue → create, drift + open issue → edit, no drift + open
# issue → close, no drift + no issue → no write).
# Workflow: schedule/workflow_dispatch only, least-privilege permissions,
# no push/pull_request trigger, never commits or pushes.
# Readers consume their whole input (here-strings, no `grep -q` on a pipe) so
# pipefail never turns a SIGPIPE into a false "missing" (ut-docs#2941).
set -euo pipefail

REAL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WF="${REAL_ROOT}/.github/workflows/docs-shots-locale-drift.yml"
WORK="$(mktemp -d)"

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox web/help/img
trap 'remove_guard_test_sandbox; rm -rf "${WORK}"' EXIT
SCRIPT="scripts/ci/docs-shots-locale-drift.sh"
# The sandbox has its files staged but no commit; collect compares with HEAD.
git -c user.name=t -c user.email=t@example.invalid commit -qm base

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }
has() { # has <haystack> <fixed string>
  grep -qF -- "$2" <<<"$1"
}

# --- collect: clean tree ----------------------------------------------------
out="$(bash "${SCRIPT}" collect "${WORK}/clean")"
if [ "${out}" = "count=0" ] && [ ! -s "${WORK}/clean/stale.txt" ] && [ ! -e "${WORK}/clean/body.md" ]; then
  pass "clean tree: count=0, empty stale list, no body"
else
  fail "clean tree: got '${out}'"
fi

# --- collect: changed + new + deleted ---------------------------------------
changed="$(git ls-files -- 'web/help/img/*/*.png' | sed -n 1p)"
deleted="$(git ls-files -- 'web/help/img/*/*.png' | sed -n 2p)"
if [ -z "${changed}" ] || [ -z "${deleted}" ]; then
  echo "FAIL - sandbox has fewer than two committed screenshots" >&2
  exit 1
fi
printf 'changed bytes' >"${changed}"
rm "${deleted}"
mkdir -p web/help/img/xx
printf 'new bytes' >web/help/img/xx/new-topic.png

out="$(bash "${SCRIPT}" collect "${WORK}/drift")"
stale="$(cat "${WORK}/drift/stale.txt")"
want="$(printf 'changed %s\ndeleted %s\nnew xx/new-topic.png\n' "${changed#web/help/img/}" "${deleted#web/help/img/}" | LC_ALL=C sort -k2)"
if [ "${out}" = "count=3" ]; then pass "drift: count=3"; else fail "drift: printed '${out}', want count=3"; fi
if [ "${stale}" = "${want}" ]; then
  pass "drift: stale list names changed/deleted/new, sorted by path"
else
  fail "drift: stale list was:"$'\n'"${stale}"$'\n'"want:"$'\n'"${want}"
fi
if [ "$(cat "${WORK}/drift/img/${changed#web/help/img/}")" = "changed bytes" ] &&
  [ "$(cat "${WORK}/drift/img/xx/new-topic.png")" = "new bytes" ] &&
  [ ! -e "${WORK}/drift/img/${deleted#web/help/img/}" ]; then
  pass "drift: regenerated files copied (deleted one not)"
else
  fail "drift: artifact copy wrong"
fi
body="$(cat "${WORK}/drift/body.md")"
if has "${body}" "**3** file(s)" && has "${body}" "new xx/new-topic.png" && has "${body}" "make docs-shots"; then
  pass "drift: body lists the files and the fix"
else
  fail "drift: body was:"$'\n'"${body}"
fi
out="$(GITHUB_RUN_ID=42 GITHUB_REPOSITORY=o/r GITHUB_SERVER_URL=https://gh.example bash "${SCRIPT}" collect "${WORK}/drift-ci")"
if has "$(cat "${WORK}/drift-ci/body.md")" "https://gh.example/o/r/actions/runs/42"; then
  pass "drift in CI: body links the run"
else
  fail "drift in CI: no run link in body"
fi

if ! (GIT_DIR=/nonexistent bash "${SCRIPT}" collect "${WORK}/git-broken" >/dev/null 2>&1); then
  pass "collect: a failing git status fails the step (never reads as no drift)"
else
  fail "collect: a failing git status was read as count=0"
fi

# --- sync-issue: stub gh ------------------------------------------------------
mkdir -p "${WORK}/bin"
cat >"${WORK}/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >>"${GH_LOG}"
if [ "$1 $2" = "issue list" ]; then printf '%s' "${GH_OPEN_ISSUE:-}"; fi
EOF
chmod +x "${WORK}/bin/gh"

sync() { # sync <out-dir> <open issue number or empty>; echoes the gh log
  local log="${WORK}/gh.log"
  : >"${log}"
  PATH="${WORK}/bin:${PATH}" GH_LOG="${log}" GH_OPEN_ISSUE="$2" bash "${SCRIPT}" sync-issue "$1" >/dev/null
  cat "${log}"
}

log="$(sync "${WORK}/drift" "")"
LIST="gh issue list --label docs-shots-drift --state open --limit 1 --json number --jq .[0].number // empty"
if has "${log}" "${LIST}" && has "${log}" "gh issue create --title" &&
  has "${log}" "--label docs-shots-drift --body-file ${WORK}/drift/body.md" &&
  grep -qE '^gh label create docs-shots-drift .*--force$' <<<"${log}" && ! has "${log}" "gh issue edit"; then
  pass "sync: drift, no open issue → label ensured, issue created"
else
  fail "sync: drift/no issue log:"$'\n'"${log}"
fi
log="$(sync "${WORK}/drift" "17")"
if has "${log}" "${LIST}" && has "${log}" "gh issue edit 17 --body-file ${WORK}/drift/body.md" && ! has "${log}" "gh issue create"; then
  pass "sync: drift, open issue → updated in place"
else
  fail "sync: drift/open issue log:"$'\n'"${log}"
fi
log="$(sync "${WORK}/clean" "17")"
if has "${log}" "${LIST}" && has "${log}" "gh issue close 17 --comment" && ! has "${log}" "gh issue create" && ! has "${log}" "gh label"; then
  pass "sync: no drift, open issue → closed"
else
  fail "sync: clean/open issue log:"$'\n'"${log}"
fi
log="$(sync "${WORK}/clean" "")"
if [ "$(grep -cv '^gh issue list' <<<"${log}" || true)" = "0" ]; then
  pass "sync: no drift, no issue → no write"
else
  fail "sync: clean/no issue wrote:"$'\n'"${log}"
fi
if ! (bash "${SCRIPT}" sync-issue "${WORK}/missing" 2>/dev/null); then
  pass "sync: refuses to run before collect"
else
  fail "sync: ran without a stale list"
fi

# --- workflow wiring ----------------------------------------------------------
wf="$(cat "${WF}")"
on_block="$(awk '/^on:/{on=1; next} on && /^[^ #]/{exit} on' "${WF}")"
if has "${on_block}" "schedule:" && has "${on_block}" "workflow_dispatch:" &&
  ! grep -qE '^[[:space:]]*(push|pull_request|pull_request_target):' <<<"${on_block}"; then
  pass "workflow: schedule + workflow_dispatch only"
else
  fail "workflow: triggers must be schedule/workflow_dispatch only:"$'\n'"${on_block}"
fi
perms="$(awk '/^permissions:/{on=1; next} on && /^[^ #]/{exit} on && /^  [a-z-]+:/' "${WF}")"
if [ "${perms}" = $'  contents: read\n  issues: write' ]; then
  pass "workflow: permissions are exactly contents: read, issues: write"
else
  fail "workflow: top-level permissions were:"$'\n'"${perms}"
fi
if ! grep -qE '^[[:space:]]+permissions:' <<<"${wf}"; then
  pass "workflow: no job-level permission widening"
else
  fail "workflow: a job sets its own permissions"
fi
if ! grep -vE '^[[:space:]]*#' <<<"${wf}" | grep -E 'git (push|commit)|contents: write|pull-requests: write' >/dev/null; then
  pass "workflow: never commits or pushes"
else
  fail "workflow: commits/pushes or asks for write access to code"
fi
if has "${wf}" "make docs-shots" && has "${wf}" "docs-shots-locale-drift.sh collect" &&
  has "${wf}" "docs-shots-locale-drift.sh sync-issue" && has "${wf}" "fonts-inter fonts-dejavu-core fonts-noto-color-emoji"; then
  pass "workflow: regenerates with the reference fonts, then collects and syncs the issue"
else
  fail "workflow: missing a step"
fi
if has "${wf}" "collect \"\$RUNNER_TEMP/docs-shots-drift\" >> \"\$GITHUB_OUTPUT\"" &&
  has "${wf}" "if: steps.drift.outputs.count != '0'" &&
  has "${wf}" "sync-issue \"\$RUNNER_TEMP/docs-shots-drift\"" &&
  has "${wf}" "GH_TOKEN: \${{ github.token }}" && has "${wf}" "GH_REPO: \${{ github.repository }}" &&
  has "${wf}" "persist-credentials: false"; then
  pass "workflow: count reaches GITHUB_OUTPUT, upload gated on it, gh has token + repo, no persisted credentials"
else
  fail "workflow: output/condition/env wiring broken"
fi
upload="$(awk '/name: Upload the regenerated files/{on=1} on && /name: Open, update or close/{exit} on' "${WF}")"
if has "${upload}" "continue-on-error: true"; then
  pass "workflow: a failed artifact upload never skips the issue update"
else
  fail "workflow: missing a step"
fi

if [ "${fails}" -ne 0 ]; then
  echo "docs-shots-locale-drift_test: ${fails} check(s) failed" >&2
  exit 1
fi
echo "docs-shots-locale-drift_test: all checks passed"
