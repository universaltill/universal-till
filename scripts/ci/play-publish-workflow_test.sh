#!/usr/bin/env bash
# Wiring guard for .github/workflows/play-publish.yml (ut-docs#6410): pins the
# properties its safety rests on.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WF="${1:-$ROOT/.github/workflows/play-publish.yml}"
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fails=$((fails + 1)); }
code() { sed -E 's/(^|[[:space:]])#.*$//' "$WF"; }
has() { code | grep -qE "$1"; }

# only trigger: workflow_dispatch
# the inline form (`on: [workflow_dispatch, push]`) must not slip past: the only
# accepted `on:` is the block form with workflow_dispatch as its one key.
if has '^on:[[:space:]]*[^[:space:]]'; then bad "inline on: form"; else ok "on: is the block form"; fi
onkeys="$(code | awk '/^on:/{f=1;next} f&&/^[^[:space:]]/{exit} f&&/^  [A-Za-z_]+:/{print $1}')"
[ "$onkeys" = "workflow_dispatch:" ] && ok "on: has exactly workflow_dispatch" || bad "on: keys: $onkeys"
if has '^[[:space:]]*(push|pull_request|pull_request_target|release|schedule|workflow_run):'; then bad "extra trigger"; else ok "workflow_dispatch is the only trigger"; fi
has '^[[:space:]]+workflow_dispatch:' && ok "workflow_dispatch present" || bad "no workflow_dispatch"
# dry_run input defaults to true
code | awk '/dry_run:/{f=1} f&&/default:/{print $2; exit}' | grep -qx true && ok "dry_run defaults to true" || bad "dry_run default"
# every uses: is SHA-pinned
unpinned="$(code | grep -E '^\s*-?\s*uses:' | grep -vE '@[0-9a-f]{40}( |$)' || true)"
[ -z "$unpinned" ] && ok "all actions pinned to SHAs" || bad "unpinned: $unpinned"
# top-level permissions read-only, id-token only in the two gated jobs
has '^permissions:' && ok "top-level permissions block" || bad "no permissions block"
[ "$(code | grep -c 'id-token: write')" = 2 ] && ok "id-token: write in exactly 2 jobs" || bad "id-token count"
# no stored credential, no self-hosted
code | grep -qE 'secrets\.|credentials_json|self-hosted' && bad "secret/credentials_json/self-hosted used" || ok "no secrets., credentials_json or self-hosted"
# environments
[ "$(code | grep -c 'environment: play-internal')" = 2 ] && ok "sign and upload run in play-internal" || bad "environments"
code | grep -qE 'environment: (play-production|google-play)' && bad "references a production environment" || ok "no production environment here"
# dry-run gates every job that signs or uploads
[ "$(code | grep -cE '^    if: \$\{\{ !inputs.dry_run')" = 2 ] && ok "sign/upload skipped on dry_run" || bad "dry_run gating"

# --- H3: the exact interface of the flavor PR (#6409) -----------------------
gr="$(code | grep -E 'gradlew' -A3 | tr '\n' ' ')"
echo "$gr" | grep -q -- '-PinstallChannel=play' && ok "gradle passes -PinstallChannel=play" || bad "gradle lacks -PinstallChannel=play"
echo "$gr" | grep -qE -- '-PversionCode="\$VERSION_BASE"' && ok "gradle gets the BASE versionCode (flavor multiplies)" || bad "gradle versionCode is not the base"
echo "$gr" | grep -q -- '-PversionCode="\$VERSION_CODE"' && bad "gradle gets the multiplied versionCode (would build base*100+55)" || ok "gradle is not given the multiplied code"
chk="$(code | grep -E 'bash .*check-aab\.sh' -A1 | grep -vE '^--$' || true)"
[ "$(echo "$chk" | grep -c -- '--allow-unsigned')" = 1 ] && ok "build check: --allow-unsigned (once, unsigned bundle only)" || bad "check-aab --allow-unsigned count"
[ "$(echo "$chk" | grep -c -- '--expect-version-code')" = 2 ] && ok "both checks pin --expect-version-code" || bad "check-aab --expect-version-code count"
[ "$(echo "$chk" | grep -c -- '--expect-cert-sha256')" = 1 ] && ok "signed check pins --expect-cert-sha256" || bad "check-aab --expect-cert-sha256 count"
echo "$chk" | grep -qE 'check-aab\.sh +"[^"]+" +"?\$' && bad "check-aab positional second argument" || ok "no positional versionCode to check-aab"
# every flag the workflow passes exists in the script (interface drift catcher)
CHK="$ROOT/android/scripts/check-aab.sh"
if [ -f "$CHK" ]; then
  for f in $(echo "$chk" | grep -oE -- '--[a-z0-9-]+' | sort -u); do
    grep -qE "^[[:space:]]+$f\)" "$CHK" && ok "check-aab.sh accepts $f" || bad "check-aab.sh has no flag $f"
  done
else bad "android/scripts/check-aab.sh missing (flavor PR not merged)"; fi
grep -q 'installChannel' "$ROOT/android/app/build.gradle.kts" && ok "flavor build.gradle.kts has installChannel" || bad "flavor not merged"

# --- H1: privileged jobs run from main, the tag is only checked out ----------
has '"\$REF" != "refs/heads/main"' && ok "non-dry runs require ref main" || bad "no main-ref requirement"
has 'verify-release-tag\.sh' && ok "tag verified by scripts/ci/verify-release-tag.sh (from main)" || bad "tag check not in a script"
code | grep -qE 'REF_TYPE|ref_type|ref_name|startsWith' && bad "workflow still trusts a tag ref / inline tag check" || ok "no tag-ref trust or inline tag check in the workflow"
code | grep -qE 'ref: \$\{\{ steps\.v\.outputs\.source_sha' && ok "tag checked out by verified SHA" || bad "no checkout of the verified SHA"
# sign + upload jobs must not execute tag-controlled code: checkouts there use the default (main) ref
sign_up="$(code | awk '/^  sign:/{f=1} f')"
echo "$sign_up" | grep -qE 'source_sha|refs/tags' && bad "sign/upload jobs check out the tag" || ok "sign/upload jobs only run main code"
# release.yml dispatches from main with the version input (never --ref "$TAG")
REL="$ROOT/.github/workflows/release.yml"
if [ -f "$REL" ]; then
  d="$(grep -E 'gh workflow run play-publish\.yml' "$REL" || true)"
  echo "$d" | grep -q -- '--ref main' && echo "$d" | grep -q -- '-f version=' && ! echo "$d" | grep -q -- '--ref "\$TAG"' && ok "release.yml dispatches play-publish from main with a version" || bad "release.yml dispatch: $d"
fi
# the bundle artifacts are deleted again by a cleanup job holding only GITHUB_TOKEN
code | grep -q '^  cleanup:' && code | grep -q 'actions: write' && ok "cleanup job removes the signed bundle artifacts" || bad "no artifact cleanup job"
# --- M3: upload cert pinned, and never the release cert --------------------
has 'play-upload-cert-sha256\.txt' && ok "upload cert pin file used" || bad "no upload-cert pin"
has 'apk-signer-sha256\.txt' && ok "release cert list compared" || bad "release cert not compared"
# --- low: artifact retention, gomobile pin ---------------------------------
[ "$(code | grep -c 'retention-days: 1$')" = 2 ] && ok "artifacts kept 1 day" || bad "retention-days not 1"
code | grep -qE '(gomobile|gobind)@latest' && bad "gomobile/gobind unpinned" || ok "gomobile/gobind pinned"
[ "$fails" = 0 ]
