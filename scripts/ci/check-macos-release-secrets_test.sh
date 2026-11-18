#!/usr/bin/env bash
#
# Behaviour test for ut-docs#6025: check-macos-release-secrets.sh refuses a
# release macOS build that lacks the Developer ID signing secrets or a complete
# notarization credential set, but only in the upstream repo (forks pass).
# Env-only: the script reads nothing from the tree and the test plants no
# files, so no sandbox is needed. Every secret is a distinct sentinel and the
# output must never contain one.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/check-macos-release-secrets.sh"

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

U="RELEASE_REPO=universaltill/universal-till"
SIGN=(MACOS_CERT_P12=SENTINEL-cert-p12 MACOS_CERT_PASSWORD=SENTINEL-cert-pw MACOS_SIGN_IDENTITY=SENTINEL-identity)
API=(MACOS_NOTARY_KEY_P8=SENTINEL-key-p8 MACOS_NOTARY_KEY_ID=SENTINEL-key-id MACOS_NOTARY_ISSUER_ID=SENTINEL-issuer)
AID=(MACOS_NOTARY_APPLE_ID=SENTINEL-apple-id MACOS_NOTARY_TEAM_ID=SENTINEL-team MACOS_NOTARY_PASSWORD=SENTINEL-notary-pw)

# run_case <want-exit> <label> <must-name,...|-> [VAR=val ...]
run_case() {
  local want="$1" label="$2" names="$3"
  shift 3
  local out rc=0 n
  out="$(env -i PATH="$PATH" "$@" bash "$SCRIPT" 2>&1)" || rc=$?
  if [ "$rc" -ne "$want" ]; then
    fail "${label}: exit ${rc}, want ${want}"
    return
  fi
  if grep -qF -- "SENTINEL" <<<"$out"; then
    fail "${label}: output leaks a secret value"
    return
  fi
  if [ "$names" != "-" ]; then
    local IFS=','
    for n in $names; do
      if ! grep -qF -- "$n" <<<"$out"; then
        fail "${label}: output does not name ${n}"
        return
      fi
    done
  fi
  pass "$label"
}

run_case 0 "1 upstream, signing + API key" - "$U" "${SIGN[@]}" "${API[@]}"
run_case 0 "2 upstream, signing + Apple ID" - "$U" "${SIGN[@]}" "${AID[@]}"
run_case 0 "3 upstream, prod shape (API + lone TEAM_ID)" - "$U" "${SIGN[@]}" "${API[@]}" MACOS_NOTARY_TEAM_ID=SENTINEL-team
run_case 1 "4a missing MACOS_CERT_P12" MACOS_CERT_P12 "$U" "${API[@]}" MACOS_CERT_PASSWORD=SENTINEL-a MACOS_SIGN_IDENTITY=SENTINEL-b
run_case 1 "4b missing MACOS_CERT_PASSWORD" MACOS_CERT_PASSWORD "$U" "${API[@]}" MACOS_CERT_P12=SENTINEL-a MACOS_SIGN_IDENTITY=SENTINEL-b
run_case 1 "4c missing MACOS_SIGN_IDENTITY" MACOS_SIGN_IDENTITY "$U" "${API[@]}" MACOS_CERT_P12=SENTINEL-a MACOS_CERT_PASSWORD=SENTINEL-b
run_case 1 "5 no signing at all" MACOS_CERT_P12,MACOS_CERT_PASSWORD,MACOS_SIGN_IDENTITY "$U" "${API[@]}"
run_case 1 "6 API trio incomplete" MACOS_NOTARY_KEY_ID,incomplete "$U" "${SIGN[@]}" MACOS_NOTARY_KEY_P8=SENTINEL-a MACOS_NOTARY_ISSUER_ID=SENTINEL-b
run_case 1 "7 Apple ID trio incomplete" MACOS_NOTARY_PASSWORD "$U" "${SIGN[@]}" MACOS_NOTARY_APPLE_ID=SENTINEL-a MACOS_NOTARY_TEAM_ID=SENTINEL-b
run_case 1 "8 no notary credentials" MACOS_NOTARY_KEY_P8 "$U" "${SIGN[@]}"
run_case 1 "9 nothing set" MACOS_SIGN_IDENTITY,MACOS_NOTARY_KEY_P8 "$U"
run_case 0 "10 fork, nothing set" fork RELEASE_REPO=someone/universal-till
run_case 0 "11 fork, all set" - RELEASE_REPO=someone/universal-till "${SIGN[@]}" "${API[@]}" "${AID[@]}"
run_case 1 "12 RELEASE_REPO unset" RELEASE_REPO
run_case 0 "13 near-miss repo name is a fork" fork RELEASE_REPO=universaltill/universal-till-fork
run_case 1 "14 whitespace-only signing secret is missing" MACOS_CERT_P12 "$U" "${API[@]}" "MACOS_CERT_P12=  " MACOS_CERT_PASSWORD=SENTINEL-a MACOS_SIGN_IDENTITY=SENTINEL-b
run_case 1 "15 whitespace-only notary secrets are missing" MACOS_NOTARY_KEY_P8 "$U" "${SIGN[@]}" "MACOS_NOTARY_KEY_P8=
" MACOS_NOTARY_KEY_ID=SENTINEL-a MACOS_NOTARY_ISSUER_ID=SENTINEL-b

if grep -qE '^[[:space:]]*set -[a-z]*x' "$SCRIPT"; then
  fail "script enables set -x (would echo secrets)"
else
  pass "script never enables set -x"
fi

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all macOS release secret preflight checks passed"
