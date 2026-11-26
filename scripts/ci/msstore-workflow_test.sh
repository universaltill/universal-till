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
has "$(cat "$WF")" 'workflow_dispatch:' "manual dispatch"
has "$(cat "$WF")" 'workflow_call:' "callable by promote-release.yml"
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
[ "$trig" = "workflow_dispatch:workflow_call:" ] && ok "workflow_dispatch + workflow_call are the only triggers" || bad "triggers: $trig"
dflt() { awk -v i="      $1:" '$0==i{on=1;next} on && /^      [a-z_]+:/{exit} on && /default:/{print $2; exit}' "$WF"; }
[ "$(dflt verify_only)" = false ] && ok "verify_only defaults to false" || bad "verify_only default"
has "$submit" '!inputs.verify_only' "submit is skipped when verify_only"
hasnt "$(cat "$WF")" 'secrets: inherit' "no secrets: inherit"
hasnt "$(grep -E '^\s+secrets:' "$WF")" 'secrets' "no secrets pass-through block"
# Safe defaults: dry run on, commit off.
[ "$(dflt dry_run)" = true ] && ok "dry_run defaults to true" || bad "dry_run default"
[ "$(dflt commit)" = false ] && ok "commit defaults to false" || bad "commit default"
# The same defaults on the workflow_call trigger (dflt reads the first block only).
cdflt() { awk -v i="      $1:" '/^  workflow_call:/{c=1;next} c && $0==i{on=1;next} on && /^      [a-z_]+:/{exit} on && /default:/{print $2; exit}' "$WF"; }
[ "$(cdflt dry_run)" = true ] && ok "workflow_call dry_run defaults to true" || bad "workflow_call dry_run default"
[ "$(cdflt commit)" = false ] && ok "workflow_call commit defaults to false" || bad "workflow_call commit default"
[ "$(cdflt verify_only)" = false ] && ok "workflow_call verify_only defaults to false" || bad "workflow_call verify_only default"
# Direct dispatch cannot skip the gates: preflight in verify and again after the approval.
[ "$(grep -c 'promote-preflight.sh' "$WF")" = 2 ] && ok "promote-preflight.sh runs in verify and in submit (post-approval)" || bad "preflight wiring"
has "$submit" 'promote-preflight.sh' "submit re-checks the gates after the approval"
hasnt "$(awk '/^concurrency:/{f=1} f' <<<"$(sed -n '1,/^jobs:/p' "$WF")")" 'concurrency:' "no workflow-level concurrency (a parked approval must not block a verify)"
has "$submit" 'group: msstore-submit-' "submit serialised per tag"
# Main-ref check inside the workflow, in the verify job, before anything else.
first="$(awk '/^    steps:/{on=1;next} on && /- name:/{print; exit}' <<<"$verify")"
has "$first" 'Store-reaching runs only from main' "first verify step is the main-ref check"
has "$verify" 'if: ${{ !inputs.verify_only }}' "main-ref check skipped only for verify_only"
has "$verify" '[ "$REF" = "refs/heads/main" ]' "main-ref check compares to refs/heads/main"
has "$verify" 'REF: ${{ github.ref }}' "ref reaches the check through env"
has "$submit" 'environment: msstore-submit' "submit runs in the msstore-submit environment"
has "$submit" 'id-token: write' "submit has job-scoped OIDC"
has "$submit" 'vars.AZURE_MSSTORE_CLIENT_ID' "federated client id variable"
has "$submit" 'vars.AZURE_TENANT_ID' "tenant variable"
has "$submit" 'vars.MSSTORE_APP_ID' "store app id variable"
has "$submit" 'allow-no-subscriptions: true' "no subscription needed"
has "$submit" 'msstore-submit.sh' "submit goes through msstore-submit.sh"
hasnt "$submit" 'client-secret' "no client secret"
hasnt "${submit//secrets.GITHUB_TOKEN/}" 'secrets.' "no repository secret beyond the job token"
# The owner opened the Microsoft Store (ut-docs#6408): marker + opening file.
grep -qE 'store-release:allow ut-docs#6408( |$)' "$WF" && ok "workflow carries the store-release:allow ut-docs#6408 marker" || bad "marker missing"
[ "$(cat store-release/microsoft.opened 2>/dev/null)" = "ut-docs#6408" ] && ok "store-release/microsoft.opened names ut-docs#6408" || bad "opening file"
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
# ut-docs#6636: test overrides are never set by the workflow (word-bounded).
for v in UT_MSSTORE_TEST SIGN_EXE SOAK_CMD SILENT_CMD SIG_VERIFY_CMD INSTALLER_NSI MSSTORE_SOAK_ALLOW_HTTP MSSTORE_ROOT; do
  ! grep -qwF -- "$v" "$WF" && ok "msstore-submit.yml never sets $v" || bad "msstore-submit.yml names the test override $v"
done
# The blob the Store fetches is the one publish-installer uploads: no drift.
base="$(sed -n 's/^INSTALLER_BASE=//p' scripts/ci/msstore-submit.endpoints)"
acct="$(sed -n 's/^ *STORAGE_ACCOUNT: *//p' .github/workflows/publish-installer.yml | sed -n 1p)"
cont="$(sed -n 's/^container="${CONTAINER:-\(.*\)}"$/\1/p' scripts/ci/publish-installer.sh)"
[ -n "$acct" ] && [ -n "$cont" ] && [ "$base" = "https://${acct}.blob.core.windows.net/${cont}" ] && ok "INSTALLER_BASE equals publish-installer account/container" || bad "INSTALLER_BASE drifted from publish-installer ($base vs $acct/$cont)"
grep -qF 'blob="windows/v${ver}/${asset}"' scripts/ci/publish-installer.sh && ok "blob path layout windows/vX.Y.Z/<asset> unchanged" || bad "publish-installer blob layout changed"
[ "$fails" = 0 ] || { echo "msstore-workflow_test: $fails failure(s)" >&2; exit 1; }
echo "✓ msstore-workflow_test: all cases passed"
