#!/usr/bin/env bash
# Wiring test for .github/workflows/promote-release.yml (ut-docs#6407).
# shellcheck disable=SC2015,SC2016
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"
WF=.github/workflows/promote-release.yml
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }
has() { grep -qF -- "$2" <<<"$1" && ok "$3" || bad "$3"; }
hasnt() { ! grep -qF -- "$2" <<<"$1" && ok "$3" || bad "$3"; }
job() { awk -v j="  $1:" '$0==j{on=1; print; next} on && /^  [a-z][a-z0-9-]*:$/{exit} on{print}' "$WF"; }
all="$(cat "$WF")"
code="$(grep -v '^ *#' "$WF")"

pre="$(job preflight)"; ms="$(job microsoft)"; stores="$(job stores)"; summary="$(job summary)"
for j in pre ms stores summary; do [ -n "${!j}" ] && ok "job $j exists" || bad "job $j missing"; done
# Triggers: workflow_dispatch only.
trig="$(awk '/^on:/{on=1;next} on && /^[a-z]/{exit} on && /^  [a-z_]+:/{print $1}' "$WF" | tr -d '\n')"
[ "$trig" = "workflow_dispatch:" ] && ok "workflow_dispatch is the only trigger" || bad "triggers: $trig"
for w in 'push:' 'pull_request' 'workflow_run' 'schedule:' 'workflow_call' 'repository_dispatch'; do hasnt "$code" "$w" "no $w"; done
grep -qE '^permissions: \{\}$' "$WF" && ok "top-level permissions are empty" || bad "top-level permissions"
# Inputs and defaults.
dflt() { awk -v i="      $1:" '$0==i{on=1;next} on && /^      [a-z_]+:/{exit} on && /default:/{print $2; exit}' "$WF"; }
[ "$(dflt dry_run)" = true ] && ok "dry_run defaults to true" || bad "dry_run default"
[ "$(dflt probe_store)" = false ] && ok "probe_store defaults to false" || bad "probe_store default"
[ "$(dflt commit)" = true ] && ok "commit defaults to true (ignored on dry run)" || bad "commit default"
has "$all" 'soak_minutes:' "soak_minutes input"
# Concurrency per version, no cancel.
has "$all" 'group: promote-release-${{ inputs.version }}' "concurrency group per version"
has "$all" 'cancel-in-progress: false' "an approval wait is never cancelled by a newer run"
# Secrets / runners.
hasnt "${code//secrets.GITHUB_TOKEN/}" 'secrets.' "no secret beyond GITHUB_TOKEN"
hasnt "$code" 'secrets: inherit' "no secrets: inherit"
hasnt "$code" 'self-hosted' "no self-hosted runner (public repo)"
[ "$(grep -c 'runs-on: ubuntu-latest' "$WF")" = 3 ] && ok "the 3 runner jobs are GitHub-hosted ubuntu-latest" || bad "runs-on count"
# Every runner job has a timeout.
for j in preflight stores summary; do has "$(job $j)" 'timeout-minutes:' "$j has timeout-minutes"; done
# Permissions: id-token once, on microsoft only; issues write only summary; actions write only stores.
[ "$(grep -v '^ *#' "$WF" | grep -c 'id-token: write')" = 1 ] && has "$ms" 'id-token: write' "id-token: write only on the microsoft call" || bad "id-token placement"
[ "$(grep -v '^ *#' "$WF" | grep -c 'issues: write')" = 1 ] && has "$summary" 'issues: write' "issues: write only on summary" || bad "issues placement"
[ "$(grep -v '^ *#' "$WF" | grep -c 'actions: write')" = 1 ] && has "$stores" 'actions: write' "actions: write only on stores" || bad "actions: write placement"
hasnt "$all" 'contents: write' "no job writes contents"
for j in "$pre" "$ms" "$stores" "$summary"; do has "$j" 'permissions:' "job declares explicit permissions"; done
# Microsoft: workflow_call of msstore-submit.yml, three modes via verify_only.
has "$ms" 'uses: ./.github/workflows/msstore-submit.yml' "microsoft calls msstore-submit.yml"
has "$ms" 'needs: preflight' "microsoft waits for the preflight"
has "$ms" 'verify_only: ${{ inputs.dry_run && !inputs.probe_store }}' "verify_only = dry_run && !probe_store"
has "$ms" 'dry_run: ${{ inputs.dry_run }}' "dry_run passed through"
has "$ms" 'commit: ${{ !inputs.dry_run && inputs.commit }}' "commit only honoured when dry_run=false"
has "$ms" 'tag: v${{ inputs.version }}' "tag = v<version>"
hasnt "$ms" 'secrets:' "no secrets passed to the called workflow"
# Preflight/stores/summary wiring.
has "$pre" 'scripts/ci/promote-preflight.sh' "preflight runs promote-preflight.sh"
has "$stores" 'needs: preflight' "stores run in parallel with the approval wait (needs preflight only)"
has "$stores" 'scripts/ci/promote-stores.sh' "stores run promote-stores.sh"
has "$summary" "if: \${{ always() }}" "summary always runs (reject/expiry/cancel too)"
has "$summary" 'needs: [preflight, microsoft, stores]' "summary needs all three"
has "$summary" 'scripts/ci/promote-summary.sh' "summary runs promote-summary.sh"
# Third-party actions SHA-pinned (first-party actions/* by tag only if pinned like play-publish: here all pinned).
while IFS= read -r l; do
  a="${l#*uses: }"; a="${a%% *}"
  case "$a" in ./.github/workflows/*) ;; *@????????????????????????????????????????) ok "pinned: ${a%%@*}" ;; *) bad "action not SHA-pinned: $a" ;; esac
done < <(grep -E '^\s*-? ?uses:' "$WF")
# No inputs/needs/steps/event/github.ref interpolated into a run script.
if awk '/^ *(- )?run:/{ind=match($0,/[^ ]/); if ($0 ~ /\$\{\{/) f=1; if ($0 ~ /run: [|>]/) r=1; else r=0; next} r{cur=match($0,/[^ ]/); if (cur<=ind && $0 !~ /^ *$/) r=0; else if ($0 ~ /\$\{\{/) f=1} END{exit !f}' "$WF"; then bad "an expression is interpolated into a run script"; else ok "no expression inside any run: script (env only)"; fi
# Test-only overrides are never set here.
for v in UT_MSSTORE_TEST SIGN_EXE SOAK_CMD SILENT_CMD SIG_VERIFY_CMD INSTALLER_NSI MSSTORE_ROOT; do
  ! grep -qwF -- "$v" "$WF" && ok "never sets $v" || bad "names the test override $v"
done
# The promote scripts never touch the Store API themselves (the identity lives in msstore-submit.yml only).
for f in scripts/ci/promote-preflight.sh scripts/ci/promote-stores.sh scripts/ci/promote-summary.sh; do
  ! grep -v '^ *#' "$f" | grep -qiE 'api\.store\.microsoft|azure/login|az account|--clobber|submit_for_review|track' && ok "$f has no Store API, Azure login, track or clobber" || bad "$f reaches a guarded path"
done
[ "$fails" = 0 ] || { echo "promote-release-workflow_test: $fails failure(s)" >&2; exit 1; }
echo "✓ promote-release-workflow_test: all cases passed"
