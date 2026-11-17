#!/usr/bin/env bash
# Wiring test for .github/workflows/msstore-submit.yml (ut-docs#6407).
# Fixture and `A && ok || fail` assertion style used throughout this test.
# shellcheck disable=SC2015
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
has "$submit" "inputs.dry_run == false" "submit only when dry_run is false"
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
# Inputs never interpolated into a script body.
if grep -nE 'run:|^\s+[a-z]' "$WF" >/dev/null && awk '/run: \|/{r=1;next} /^      - /{r=0} r && /\$\{\{ *inputs\./{f=1} END{exit !f}' "$WF"; then bad "an input is interpolated into a run script"; else ok "inputs reach scripts only through env"; fi
# The CA root pin is the same one release signing uses.
a="$(grep -E '^MS_ROOT_SHA256=' packaging/windows/setup-signing.sh)"; b="$(grep -E '^MS_ROOT_SHA256=' packaging/windows/setup-verify.sh)"
[ -n "$a" ] && [ "$a" = "$b" ] && ok "setup-verify.sh pins the same Microsoft root" || bad "root pin differs"
[ "$fails" = 0 ] || { echo "msstore-workflow_test: $fails failure(s)" >&2; exit 1; }
echo "✓ msstore-workflow_test: all cases passed"
