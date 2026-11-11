#!/usr/bin/env bash
#
# Wiring test for ut-docs#6025: the macos-app job refuses to build an ad-hoc /
# un-notarized release .dmg (#6024 promises a signed, notarized one).
#
# Pins, in .github/workflows/release.yml's macos-app job:
#   - a preflight step runs scripts/ci/check-macos-release-secrets.sh, before
#     the release wait, the certificate import and the build;
#   - it passes RELEASE_REPO and all nine signing/notary secrets, and is not
#     conditioned (no if:, no continue-on-error, no event_name) so tag pushes
#     and workflow_dispatch are both covered;
#   - the build step sets MACOS_REQUIRE_NOTARIZED upstream, which make-dmg.sh
#     enforces;
#   - macos-dmg-attach no longer claims the old "when notary credentials
#     exist" fallback.
# A mutation self-test proves each pin can fail. Mutants are mktemp copies
# outside the tree (never the real tree, #4311), so no sandbox is needed.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
REAL_WF=".github/workflows/release.yml"
STEP_NAME="Refuse an unsigned or un-notarized release .dmg"
SECRETS=(MACOS_CERT_P12 MACOS_CERT_PASSWORD MACOS_SIGN_IDENTITY MACOS_NOTARY_KEY_P8
  MACOS_NOTARY_KEY_ID MACOS_NOTARY_ISSUER_ID MACOS_NOTARY_APPLE_ID MACOS_NOTARY_TEAM_ID
  MACOS_NOTARY_PASSWORD)
REQ_LINE="MACOS_REQUIRE_NOTARIZED: \${{ github.repository == 'universaltill/universal-till' && '1' || '' }}"

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

# job_block <file> <job>; step_block <block> <step-name> (name line to the next step).
job_block() {
  awk -v j="  ${2}:" '$0==j{on=1; print; next} on && /^  [a-zA-Z_][a-zA-Z0-9_-]*:$/{exit} on{print}' "$1"
}
step_block() {
  awk -v n="      - name: ${2}" '
    $0==n{on=1; print; next}
    on && /^      - /{on=0}
    on{print}' <<<"$1"
}

# check_wf <file>: run every assertion; print results; return the failure count.
check_wf() {
  local wf="$1" before="$fails" job step attach name l_pre l_wait l_imp l_build build
  job="$(job_block "$wf" macos-app)"
  step="$(step_block "$job" "$STEP_NAME")"

  if grep -qE '^        run: bash scripts/ci/check-macos-release-secrets\.sh$' <<<"$step"; then
    pass "preflight step runs check-macos-release-secrets.sh"
  else
    fail "macos-app has no '${STEP_NAME}' step running scripts/ci/check-macos-release-secrets.sh"
  fi

  l_pre="$(grep -nF -- "- name: ${STEP_NAME}" <<<"$job" | cut -d: -f1 || true)"
  l_wait="$(grep -nF -- '- name: Wait for the GitHub release to exist' <<<"$job" | cut -d: -f1 || true)"
  l_imp="$(grep -nF -- '- name: Import signing certificate' <<<"$job" | cut -d: -f1 || true)"
  l_build="$(grep -nF -- '- name: Build .app and .dmg' <<<"$job" | cut -d: -f1 || true)"
  if [ -n "$l_pre" ] && [ -n "$l_wait" ] && [ -n "$l_imp" ] && [ -n "$l_build" ] \
    && [ "$l_pre" -lt "$l_wait" ] && [ "$l_wait" -lt "$l_imp" ] && [ "$l_imp" -lt "$l_build" ]; then
    pass "preflight runs before the release wait, cert import and build"
  else
    fail "ordering: preflight (${l_pre:-?}) < wait (${l_wait:-?}) < import (${l_imp:-?}) < build (${l_build:-?}) not satisfied"
  fi

  for name in "${SECRETS[@]}"; do
    if grep -qF -- "${name}: \${{ secrets.${name} }}" <<<"$step"; then
      pass "preflight passes ${name}"
    else
      fail "preflight step does not pass ${name}"
    fi
  done
  # shellcheck disable=SC2016
  # the ${{ }} is a literal GitHub expression to match, not a shell expansion
  if grep -qF -- 'RELEASE_REPO: ${{ github.repository }}' <<<"$step"; then
    pass "preflight passes RELEASE_REPO"
  else
    fail "preflight step does not pass RELEASE_REPO: \${{ github.repository }}"
  fi

  if [ -n "$step" ] && ! grep -qE '^[[:space:]]+if:|continue-on-error|github\.event_name' <<<"$step"; then
    pass "preflight is unconditional (no if:, continue-on-error, event_name)"
  else
    fail "preflight step is conditioned or missing (if:/continue-on-error/github.event_name)"
  fi

  build="$(step_block "$job" "Build .app and .dmg")"
  if grep -qF -- "$REQ_LINE" <<<"$build"; then
    pass "build step sets MACOS_REQUIRE_NOTARIZED upstream"
  else
    fail "build step lacks ${REQ_LINE}"
  fi

  if grep -qF 'UPSTREAM_REPO="universaltill/universal-till"' scripts/ci/check-macos-release-secrets.sh; then
    pass "script pins the upstream repo"
  else
    fail "check-macos-release-secrets.sh lacks UPSTREAM_REPO=\"universaltill/universal-till\""
  fi
  if [ "$(grep -cF MACOS_REQUIRE_NOTARIZED packaging/macos/make-dmg.sh)" -ge 2 ]; then
    pass "make-dmg.sh enforces MACOS_REQUIRE_NOTARIZED"
  else
    fail "make-dmg.sh must reference MACOS_REQUIRE_NOTARIZED in the identity check and the no-notary branch"
  fi

  attach="$(job_block "$wf" macos-dmg-attach)"
  if [ -n "$attach" ] && ! grep -qF 'when notary credentials exist' <<<"$attach" \
    && ! grep -qF 'when notary credentials exist' <<<"$(grep -B6 -F '  macos-dmg-attach:' "$wf" || true)"; then
    pass "macos-dmg-attach has no 'when notary credentials exist' fallback claim"
  else
    fail "macos-dmg-attach still says 'when notary credentials exist' (or is missing)"
  fi

  return $((fails - before))
}

check_wf "$REAL_WF" || true

# ---- mutation self-test (AC6) ----
tmpd="$(mktemp -d)"
trap 'rm -rf "$tmpd"' EXIT

# drop_step <in> <out>: remove the preflight step.
drop_step() {
  awk -v n="      - name: ${STEP_NAME}" '
    $0==n{skip=1; next}
    skip && /^      - /{skip=0}
    !skip{print}' "$1" >"$2"
}
# in_step_delete <in> <out> <pattern>: delete matching lines inside the preflight step only.
in_step_delete() {
  awk -v n="      - name: ${STEP_NAME}" -v p="$3" '
    $0==n{on=1; print; next}
    on && /^      - /{on=0}
    on && index($0,p){next}
    {print}' "$1" >"$2"
}

mutate_expect_caught() { # <label> <mutant-file>
  local saved="$fails" rc=0
  check_wf "$2" >/dev/null 2>&1 || rc=$?
  fails="$saved"
  if [ "$rc" -gt 0 ]; then
    pass "self-test: ${1} caught"
  else
    fail "self-test: ${1} not caught"
  fi
}

m="$tmpd/m1.yml"; drop_step "$REAL_WF" "$m"
mutate_expect_caught "M1 preflight removed" "$m"

m="$tmpd/m2.yml"
awk -v n="      - name: ${STEP_NAME}" '
  $0==n{skip=1}
  skip && $0!=n && /^      - /{skip=0}
  skip{print > blk; next}
  {print}' blk="$tmpd/blk" "$REAL_WF" >"$tmpd/m2.base"
awk -v blk="$tmpd/blk" '
  /^      - name: The \.app in the \.dmg must report the real version, never "dev"$/{
    while ((getline l < blk) > 0) print l
  }
  {print}' "$tmpd/m2.base" >"$m"
# the moved step must really sit after Build (else the mutant is vacuous)
if ! grep -qF -- "- name: ${STEP_NAME}" "$m"; then
  fail "self-test: M2 mutant construction failed"
fi
mutate_expect_caught "M2 preflight moved after build" "$m"

i=0
for name in "${SECRETS[@]}"; do
  i=$((i + 1))
  m="$tmpd/m3-${i}.yml"
  in_step_delete "$REAL_WF" "$m" "${name}: "
  if cmp -s "$REAL_WF" "$m"; then fail "self-test: M3 ${name} mutant is a no-op"; fi
  mutate_expect_caught "M3 ${name} removed from preflight" "$m"
done

m="$tmpd/m4.yml"; in_step_delete "$REAL_WF" "$m" 'RELEASE_REPO: '
mutate_expect_caught "M4 RELEASE_REPO removed" "$m"

m="$tmpd/m5.yml"
awk -v n="      - name: ${STEP_NAME}" '{print} $0==n{print "        if: github.event_name == '"'"'push'"'"'"}' "$REAL_WF" >"$m"
mutate_expect_caught "M5 event-gated preflight" "$m"

m="$tmpd/m6.yml"
awk -v r="          ${REQ_LINE}" '$0==r{print "          MACOS_REQUIRE_NOTARIZED: '"''"'"; next} {print}' "$REAL_WF" >"$m"
if cmp -s "$REAL_WF" "$m"; then fail "self-test: M6 mutant is a no-op"; fi
mutate_expect_caught "M6 MACOS_REQUIRE_NOTARIZED neutered" "$m"

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all release macOS signing-required checks passed"
