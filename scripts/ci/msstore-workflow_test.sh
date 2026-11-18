#!/usr/bin/env bash
# Wiring test for .github/workflows/msstore-submit.yml (ut-docs#6407).
# Fixture and `A && ok || fail` assertion style used throughout this test.
# shellcheck disable=SC2015,SC2016
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"
WF=.github/workflows/msstore-submit.yml
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }
has() { grep -qF -- "$2" <<<"$1" && ok "$3" || bad "$3"; }
hasnt() { ! grep -qF -- "$2" <<<"$1" && ok "$3" || bad "$3"; }
job() { awk -v j="  $1:" '$0==j{on=1; print; next} on && /^  [a-z][a-z0-9-]*:$/{exit} on{print}' "$WF"; }

verify="$(job verify)"; submit="$(job submit)"
[ -n "$verify" ] && [ -n "$submit" ] && ok "both jobs exist" || bad "jobs missing"
# Top level: no ambient permissions.
grep -qE '^permissions: \{\}$' "$WF" && ok "top-level permissions are empty" || bad "top-level permissions"
has "$(cat "$WF")" 'workflow_dispatch:' "manual dispatch only"
hasnt "$(cat "$WF")" 'push:' "no push trigger"
hasnt "$(cat "$WF")" 'workflow_run' "no workflow_run trigger"
hasnt "$(cat "$WF")" 'pull_request' "no pull_request trigger"
hasnt "$(cat "$WF")" 'schedule:' "no schedule trigger"
# Inputs.
for want in 'tag:' 'dry_run:' 'default: true' 'soak_minutes:' 'commit:'; do has "$(cat "$WF")" "$want" "input ${want}"; done
# verify: no environment, no id-token, read-only.
hasnt "$verify" 'environment:' "verify has no environment"
hasnt "$verify" 'id-token' "verify has no OIDC"
hasnt "${verify//secrets.GITHUB_TOKEN/}" 'secrets.' "verify uses no secret but the job token"
has "$verify" 'contents: read' "verify is read-only"
has "$verify" 'msstore-verify.sh' "verify runs msstore-verify.sh"
has "$verify" 'setup-verify.sh' "verify uses the no-login verification setup"
hasnt "$verify" 'azure/login' "verify never logs in to Azure"
# submit: gated, federated, least privilege.
has "$submit" "needs: verify" "submit waits for verify"
has "$submit" "needs.verify.outputs.ready == 'true'" "submit only when verify said ready"
has "$submit" 'DRY_RUN: ${{ inputs.dry_run }}' "the dry-run flag reaches the script"
has "$submit" 'EXPECT_SHA256: ${{ needs.verify.outputs.sha256 }}' "the verified digest reaches the script"
has "$submit" 'MSSTORE_SELLER_ID' "seller id reaches the script"
has "$submit" '::add-mask::' "token is masked"
has "$submit" 'rm -f "$RUNNER_TEMP/msstore-token"' "token file is dropped"
has "$submit" 'contents: read' "submit is read-only on contents"
hasnt "$(cat "$WF")" 'contents: write' "no job writes contents"
hasnt "$(cat "$WF")" 'self-hosted' "no self-hosted runner (public repo)"
[ "$(grep -c 'runs-on: ubuntu-latest' "$WF")" = 2 ] && ok "both jobs on GitHub-hosted ubuntu-latest" || bad "runs-on"
# The verify job must stop the pipeline: its script failure is not swallowed.
hasnt "$verify" 'continue-on-error' "verify cannot be tolerated"
hasnt "$submit" 'continue-on-error' "submit cannot be tolerated"
[ "$(grep -c 'id-token: write' "$WF")" = 1 ] && ok "exactly one job holds OIDC" || bad "id-token count"
# Triggers: the only key under on: is workflow_dispatch.
trig="$(awk '/^on:/{on=1;next} on && /^[a-z]/{exit} on && /^  [a-z_]+:/{print $1}' "$WF" | tr -d '\n')"
[ "$trig" = "workflow_dispatch:" ] && ok "workflow_dispatch is the only trigger" || bad "triggers: $trig"
# Safe defaults: dry run on, commit off.
dflt() { awk -v i="      $1:" '$0==i{on=1;next} on && /^      [a-z_]+:/{exit} on && /default:/{print $2; exit}' "$WF"; }
[ "$(dflt dry_run)" = true ] && ok "dry_run defaults to true" || bad "dry_run default"
[ "$(dflt commit)" = false ] && ok "commit defaults to false" || bad "commit default"
has "$submit" 'environment: msstore-submit' "submit runs in the msstore-submit environment"
has "$submit" 'id-token: write' "submit has job-scoped OIDC"
has "$submit" 'vars.AZURE_MSSTORE_CLIENT_ID' "federated client id variable"
has "$submit" 'vars.AZURE_TENANT_ID' "tenant variable"
has "$submit" 'vars.MSSTORE_APP_ID' "store app id variable"
has "$submit" 'allow-no-subscriptions: true' "no subscription needed"
has "$submit" 'msstore-submit.sh' "submit goes through msstore-submit.sh"
hasnt "$submit" 'client-secret' "no client secret"
hasnt "${submit//secrets.GITHUB_TOKEN/}" 'secrets.' "no repository secret beyond the job token"
# No opening marker before the owner's card.
hasnt "$(grep -v '^ *#' "$WF")" 'store-release:allow' "no store-release:allow marker before ut-docs#6408"
# Third-party actions pinned by full SHA (actions/* first-party tags as release.yml).
while IFS= read -r l; do
  a="${l#*uses: }"; a="${a%% *}"
  case "$a" in actions/*) ;; *@????????????????????????????????????????) ok "pinned: ${a%%@*}" ;; *) bad "third-party action not SHA-pinned: $a" ;; esac
done < <(grep -E '^\s*-? ?uses:' "$WF")
# Nothing from inputs/event/needs/steps is interpolated into any run script (single-line or block).
if awk '/^ *(- )?run:/{ind=match($0,/[^ ]/); if ($0 ~ /\$\{\{ *(inputs|github\.event|needs|steps)\./) f=1; if ($0 ~ /run: [|>]/) r=1; else r=0; next} r{cur=match($0,/[^ ]/); if (cur<=ind && $0 !~ /^ *$/) r=0; else if ($0 ~ /\$\{\{ *(inputs|github\.event|needs|steps)\./) f=1} END{exit !f}' "$WF"; then bad "a value is interpolated into a run script"; else ok "values reach scripts only through env"; fi
# The CA root pin is the same one release signing uses.
a="$(grep -E '^MS_ROOT_SHA256=' packaging/windows/setup-signing.sh)"; b="$(grep -E '^MS_ROOT_SHA256=' packaging/windows/setup-verify.sh)"
[ -n "$a" ] && [ "$a" = "$b" ] && ok "setup-verify.sh pins the same Microsoft root" || bad "root pin differs"
[ "$fails" = 0 ] || { echo "msstore-workflow_test: $fails failure(s)" >&2; exit 1; }
echo "✓ msstore-workflow_test: all cases passed"
