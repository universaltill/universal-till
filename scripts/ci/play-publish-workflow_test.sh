#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016
# Wiring guard for .github/workflows/play-publish.yml (ut-docs#6410): pins the
# properties its safety rests on.
set -eu # no pipefail: the checks pipe a captured text into grep -q, whose early exit would SIGPIPE the writer on Linux
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WF="${1:-$ROOT/.github/workflows/play-publish.yml}"
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fails=$((fails + 1)); }
code() { sed -E 's/(^|[[:space:]])#.*$//' "$WF"; }
has() { code | grep -qE "$1"; }  # pipefail-reader:allow small bounded text, writer exits before the pipe fills

# only trigger: workflow_dispatch
# the inline form (`on: [workflow_dispatch, push]`) must not slip past: the only
# accepted `on:` is the block form with workflow_dispatch as its one key.
if has '^on:[[:space:]]*[^[:space:]]'; then bad "inline on: form"; else ok "on: is the block form"; fi
onkeys="$(code | awk '/^on:/{f=1;next} f&&/^[^[:space:]]/{exit} f&&/^  [A-Za-z_]+:/{print $1}')"
[ "$onkeys" = "workflow_dispatch:" ] && ok "on: has exactly workflow_dispatch" || bad "on: keys: $onkeys"
if has '^[[:space:]]*(push|pull_request|pull_request_target|release|schedule|workflow_run):'; then bad "extra trigger"; else ok "workflow_dispatch is the only trigger"; fi
has '^[[:space:]]+workflow_dispatch:' && ok "workflow_dispatch present" || bad "no workflow_dispatch"
# dry_run input defaults to true
code | awk '/dry_run:/{f=1} f&&/default:/{print $2; exit}' | grep -qx true && ok "dry_run defaults to true" || bad "dry_run default"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# every uses: is SHA-pinned
unpinned="$(code | grep -E '^\s*-?\s*uses:' | grep -vE '@[0-9a-f]{40}( |$)' || true)"
[ -z "$unpinned" ] && ok "all actions pinned to SHAs" || bad "unpinned: $unpinned"
# top-level permissions read-only, id-token only in the two gated jobs
has '^permissions:' && ok "top-level permissions block" || bad "no permissions block"
[ "$(code | grep -c 'id-token: write')" = 2 ] && ok "id-token: write in exactly 2 jobs" || bad "id-token count"
# no stored credential, no self-hosted
code | grep -qE 'secrets\.|credentials_json|self-hosted' && bad "secret/credentials_json/self-hosted used" || ok "no secrets., credentials_json or self-hosted"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# environments
[ "$(code | grep -c 'environment: play-internal')" = 2 ] && ok "sign and upload run in play-internal" || bad "environments"
code | grep -qE 'environment: (play-production|google-play)' && bad "references a production environment" || ok "no production environment here"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# dry-run gates every job that signs or uploads
[ "$(code | grep -cE '^    if: \$\{\{ !inputs.dry_run')" = 2 ] && ok "sign/upload skipped on dry_run" || bad "dry_run gating"

# --- H3: the exact interface of the flavor PR (#6409) -----------------------
gr="$(code | grep -E 'gradlew' -A3 | tr '\n' ' ')"
echo "$gr" | grep -q -- '-PinstallChannel=play' && ok "gradle passes -PinstallChannel=play" || bad "gradle lacks -PinstallChannel=play"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
echo "$gr" | grep -qE -- '-PversionCode="\$VERSION_BASE"' && ok "gradle gets the BASE versionCode (flavor multiplies)" || bad "gradle versionCode is not the base"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
echo "$gr" | grep -q -- '-PversionCode="\$VERSION_CODE"' && bad "gradle gets the multiplied versionCode (would build base*100+55)" || ok "gradle is not given the multiplied code"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
chk="$(code | grep -E 'bash .*check-aab\.sh' -A1 | grep -vE '^--$' || true)"
[ "$(echo "$chk" | grep -c -- '--allow-unsigned')" = 1 ] && ok "build check: --allow-unsigned (once, unsigned bundle only)" || bad "check-aab --allow-unsigned count"
[ "$(echo "$chk" | grep -c -- '--expect-version-code')" = 2 ] && ok "both checks pin --expect-version-code" || bad "check-aab --expect-version-code count"
[ "$(echo "$chk" | grep -c -- '--expect-cert-sha256')" = 1 ] && ok "signed check pins --expect-cert-sha256" || bad "check-aab --expect-cert-sha256 count"
echo "$chk" | grep -qE 'check-aab\.sh +"[^"]+" +"?\$' && bad "check-aab positional second argument" || ok "no positional versionCode to check-aab"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
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
code | grep -qE 'REF_TYPE|ref_type|ref_name|startsWith' && bad "workflow still trusts a tag ref / inline tag check" || ok "no tag-ref trust or inline tag check in the workflow"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
code | grep -qE 'ref: \$\{\{ steps\.v\.outputs\.source_sha' && ok "tag checked out by verified SHA" || bad "no checkout of the verified SHA"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# sign + upload jobs must not execute tag-controlled code: checkouts there use the default (main) ref
sign_up="$(code | awk '/^  sign:/{f=1} f')"
echo "$sign_up" | grep -qE 'source_sha|refs/tags' && bad "sign/upload jobs check out the tag" || ok "sign/upload jobs only run main code"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# release.yml dispatches from main with the version input (never --ref "$TAG")
REL="${REL:-$ROOT/.github/workflows/release.yml}"
if [ -f "$REL" ]; then
  d="$(grep -E 'gh workflow run play-publish\.yml' "$REL" || true)"
  echo "$d" | grep -q -- '--ref main' && echo "$d" | grep -q -- '-f version=' && ! echo "$d" | grep -q -- '--ref "\$TAG"' && ok "release.yml dispatches play-publish from main with a version" || bad "release.yml dispatch: $d"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
fi

# --- F11 (hardening): structural allow-lists, per job ------------------------
job_names="$(code | awk '/^jobs:/{f=1;next} f&&/^  [a-z_-]+:/{gsub(":","",$1);print $1}' | tr '\n' ' ')"
[ "$job_names" = "build sign upload cleanup " ] && ok "jobs are exactly build sign upload cleanup" || bad "jobs: $job_names"
job_text() { code | awk -v j="  $1:" '/^jobs:/{f=1;next} f&&$0==j{g=1;next} f&&g&&/^  [a-z_-]+:/{exit} g'; }
for j in build sign upload cleanup; do
  ro="$(job_text "$j" | grep -E '^    runs-on:' | sed -E 's/^    runs-on:[[:space:]]*//')"
  [ "$ro" = "ubuntu-latest" ] && ok "$j runs on ubuntu-latest" || bad "$j runs-on is '$ro'"
  envs="$(job_text "$j" | grep -cE '^[[:space:]]*environment:' || true)"
  idt="$(job_text "$j" | grep -c 'id-token:' || true)"
  case "$j" in
    sign|upload)
      job_text "$j" | grep -qE '^    environment: play-internal$' && [ "$envs" = 1 ] && ok "$j: environment play-internal (string form)" || bad "$j: environment not exactly 'play-internal'"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
      [ "$idt" = 1 ] && ok "$j: id-token once" || bad "$j: id-token count $idt" ;;
    *)
      [ "$envs" = 0 ] && ok "$j: no environment" || bad "$j has an environment"
      [ "$idt" = 0 ] && ok "$j: no id-token" || bad "$j has id-token" ;;
  esac
done
co="$(code | grep -c 'actions/checkout@')"; pc="$(code | grep -c 'persist-credentials: false')"
[ "$co" -ge 3 ] && [ "$co" = "$pc" ] && ok "every checkout has persist-credentials: false ($co)" || bad "checkouts $co vs persist-credentials false $pc"
code | awk '/^concurrency:/{f=1;next} f&&/^[^[:space:]]/{exit} f' | grep -q 'group:.*inputs.dry_run' && ok "concurrency group includes dry_run" || bad "concurrency group lacks dry_run"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# the tag verification lives in the script, which must keep both halves
VT="${VT:-$ROOT/scripts/ci/verify-release-tag.sh}"
grep -q '\^\[0-9\]+\\.\[0-9\]+\\.\[0-9\]+\$' "$VT" && ok "verify-release-tag.sh: X.Y.Z regex" || bad "verify-release-tag.sh lost the semver regex"
grep -q 'merge-base --is-ancestor' "$VT" && ok "verify-release-tag.sh: ancestor-of-main check" || bad "verify-release-tag.sh lost the ancestor check"
# versionCode bounds (Play max 2100000000; minor/patch <= 999)
has 'MI" -gt 999' && has 'PA" -gt 999' && has '2100000000' && ok "version bounds checked" || bad "version bounds missing"
# release.yml: dispatch gated by the repository variable set only after the apply
if [ -f "$REL" ]; then
  code_rel() { sed -E 's/(^|[[:space:]])#.*$//' "$REL"; }
  code_rel | grep -B12 'gh workflow run play-publish' | grep -q "vars.PLAY_PUBLISH_ENABLED == 'true'" && ok "release.yml dispatch gated by vars.PLAY_PUBLISH_ENABLED" || bad "release.yml dispatch not gated"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
fi
# the bundle artifacts are deleted again by a cleanup job holding only GITHUB_TOKEN
code | grep -q '^  cleanup:' && code | grep -q 'actions: write' && ok "cleanup job removes the signed bundle artifacts" || bad "no artifact cleanup job"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# --- M3: upload cert pinned, and never the release cert --------------------
has 'play-upload-cert-sha256\.txt' && ok "upload cert pin file used" || bad "no upload-cert pin"
has 'apk-signer-sha256\.txt' && ok "release cert list compared" || bad "release cert not compared"
# --- low: artifact retention, gomobile pin ---------------------------------
[ "$(code | grep -c 'retention-days: 7$')" = 2 ] && ok "artifacts kept 7 days (an owner approval may come days later; cleanup deletes them after the upload)" || bad "retention-days not 7"
code | grep -qE '(gomobile|gobind)@latest' && bad "gomobile/gobind unpinned" || ok "gomobile/gobind pinned"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# --- N5: the pins are checked BEFORE the key is fetched; missing list fails closed
code | grep -q 'Check the pins before any key is touched' && ok "pin pre-check step exists" || bad "no pin pre-check step"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
pre="$(code | grep -n 'Check the pins before any key is touched' | head -1 | cut -d: -f1)"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
login="$(code | grep -n 'Azure login' | head -1 | cut -d: -f1)"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
[ -n "$pre" ] && [ -n "$login" ] && [ "$pre" -lt "$login" ] && ok "pin pre-check runs before the Azure login" || bad "pin pre-check is not before the Azure login"
has '\[ -s android/app/apk-signer-sha256\.txt \]' && ok "missing/empty apk-signer-sha256.txt fails closed" || bad "no -s check on apk-signer-sha256.txt"
code | grep -qE 'AZURE_SIGNING_VAULT_NAME' && bad "old shared vault variable still used" || ok "no shared-vault variable"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
has 'AZURE_PLAY_UPLOAD_VAULT_NAME' && ok "upload vault variable used" || bad "no AZURE_PLAY_UPLOAD_VAULT_NAME"

# --- review fix round 2 (findings 4, 5) ---------------------------------------
# top-level permissions are exactly contents: read (the build job runs the tag's Gradle build)
tp="$(code | awk '/^permissions:/{f=1;next} f&&/^[^[:space:]]/{exit} f{print}' | sed -E 's/^[[:space:]]+//' | tr '\n' '|')"
[ "$tp" = "contents: read|" ] && ok "top-level permissions are exactly contents: read" || bad "top-level permissions: $tp"
# sign and upload never choose a ref (default = the dispatched main), and run no code from the artifact
for j in sign upload; do
  job_text "$j" | grep -qE '^[[:space:]]+ref:' && bad "$j job sets a checkout ref" || ok "$j: no checkout ref (main code only)"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
  job_text "$j" | grep -qE '(^|[[:space:];&|(])(bash|sh|source|\.)[[:space:]]+"?\$?\{?(RUNNER_TEMP|AAB|\{\{)' && bad "$j runs a script out of RUNNER_TEMP/artifact" || ok "$j: no script run from RUNNER_TEMP/artifact"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
  job_text "$j" | grep -qE '&&[[:space:]]*(true|false)\b|(^|[[:space:]])(if|while)[[:space:]]+(true|false)\b|(^|[[:space:]])(true|false)[[:space:]]*(\|\||&&)' && bad "$j has a neutered condition (true/false short-circuit)" || ok "$j: no true/false short-circuit"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
done
job_text upload | grep -qE '^        run: bash scripts/ci/play-upload\.sh$' && ok "upload runs exactly scripts/ci/play-upload.sh from the checkout" || bad "upload run line changed"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
# the sign job's cert checks are live, unconditional lines
job_text sign | grep -qF -- '--expect-version-code "$VERSION_CODE" --expect-cert-sha256 "$pin")"' && ok "sign: check-aab pins version code and upload cert" || bad "sign: check-aab pin line changed"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
job_text sign | grep -qF 'grep -E '"'"'^[0-9a-f]{64}$'"'"' android/app/apk-signer-sha256.txt | grep -qx "$signer"' && ok "sign: signer compared against the release certs" || bad "sign: release-cert comparison missing"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
job_text sign | grep -qE '^          if grep -E .*apk-signer-sha256|^          if \[ -n "\$signer" \] && grep' && ok "sign: release-cert comparison is a plain if" || bad "sign: release-cert comparison not a plain if"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
job_text sign | grep -qF 'Check the pins before any key is touched' && ok "sign: pin pre-check step present" || bad "sign: pin pre-check step missing"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills

# --- ut-docs#6410: pin pre-check script, exact owner-command fail messages ------
CP="${CP:-$ROOT/scripts/ci/check-play-pins.sh}"
job_text sign | grep -qE '^        run: bash scripts/ci/check-play-pins\.sh$' && ok "sign: pre-check is the shared script, no --warn-only" || bad "sign: pre-check not the shared script"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
job_text build | grep -qF 'bash scripts/ci/check-play-pins.sh --warn-only' && ok "build: pin pre-check warns only on a dry run" || bad "build: no warn-only pin pre-check"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
pre_step="$(job_text build | awk '/- name: Pin pre-check/{f=1;next} f&&/^      - /{exit} f')"
echo "$pre_step" | grep -qE '^          DRY_RUN: \$\{\{ inputs\.dry_run \}\}$' && echo "$pre_step" | tr '\n' ' ' | grep -qE 'if \[ "\$DRY_RUN" = "true" \]; then +bash scripts/ci/check-play-pins\.sh --warn-only +else +bash scripts/ci/check-play-pins\.sh +fi' && ok "build: --warn-only only in the dry_run branch" || bad "build: pre-check not gated on dry_run"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
bpre="$(code | grep -n 'Pin pre-check (warn only on a dry run)' | head -1 | cut -d: -f1)"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
bndk="$(code | grep -n 'sdkmanager' | head -1 | cut -d: -f1)"  # pipefail-reader:allow small bounded text, writer exits before the pipe fills
[ -n "$bpre" ] && [ -n "$bndk" ] && [ "$bpre" -lt "$bndk" ] && ok "build: pin pre-check runs before the NDK/SDK install" || bad "build: pin pre-check not before the SDK install"
# the three owner-facing errors name the script (the script holds two, the verify step one)
grep -q "android/app/play-upload-cert-sha256.txt holds no upload-cert pin. Owner: run 'bash scripts/ci/set-play-upload-pin.sh <upload.keystore>'" "$CP" && ok "script: empty-pin error names set-play-upload-pin.sh" || bad "script: empty-pin message"
grep -q "::error::the upload-cert pin equals a release signer.*set-play-upload-pin.sh --replace <upload.keystore>" "$CP" && ok "script: release-equal error names set-play-upload-pin.sh --replace" || bad "script: release-equal message"
grep -q "apk-signer-sha256.txt is missing or empty; it is committed in the repo, restore it" "$CP" && ok "script: missing-list error says restore" || bad "script: missing-list message"
grep -q "holds no upload-cert pin. Owner: run 'bash scripts/ci/set-play-upload-pin.sh <upload.keystore>'" "$WF" && ok "verify step: empty-pin error names set-play-upload-pin.sh" || bad "verify step: empty-pin message"
grep -q 'RELEASE cert, not the upload key; load the upload keystore under the play-upload-\* vault secrets' "$WF" && ok "verify step: release-cert error says how to fix" || bad "verify step: release-cert message"
grep -q 'is missing or empty; it is committed in the repo, restore it' "$WF" && ok "verify step: missing-list error says restore" || bad "verify step: missing-list message"
[ "$fails" = 0 ]
