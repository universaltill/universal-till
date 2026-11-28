#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2181
# Proves play-publish-workflow_test.sh has teeth (review F11): each mutation
# removes one protection from a COPY of the workflow / release.yml /
# verify-release-tag.sh; the wiring test must then fail. Any mutant that still
# passes (or does not change the file) fails this script.
set -eu # no pipefail: the checks pipe a captured text into grep -q, whose early exit would SIGPIPE the writer on Linux
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
T="$ROOT/scripts/ci/play-publish-workflow_test.sh"
W="$ROOT/.github/workflows/play-publish.yml"; R="$ROOT/.github/workflows/release.yml"; V="$ROOT/scripts/ci/verify-release-tag.sh"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
fails=0
base() { WF="$W" bash "$T" "$W" >/dev/null 2>&1; }
base || { echo "FAIL - the unmutated files do not pass the wiring test"; exit 1; }
# mut <name> <file: W|R|V> <python expression on s>
mut() {
  local name="$1" which="$2" expr="$3" src out
  case "$which" in W) src="$W" ;; R) src="$R" ;; V) src="$V" ;; esac
  out="$tmp/$name"
  python3 -I - "$src" "$out" "$expr" <<'PY'
import sys
s = open(sys.argv[1]).read()
n = eval(sys.argv[3])
if n == s: print("NOCHANGE"); sys.exit(3)
open(sys.argv[2], "w").write(n)
PY
  [ $? = 0 ] || { echo "FAIL - mutation $name changed nothing"; fails=$((fails + 1)); return; }
  case "$which" in
    W) wf="$out"; rel="$R"; vt="$V" ;; R) wf="$W"; rel="$out"; vt="$V" ;; V) wf="$W"; rel="$R"; vt="$out" ;;
  esac
  if REL="$rel" VT="$vt" bash "$T" "$wf" >/dev/null 2>&1; then echo "FAIL - mutant survived: $name"; fails=$((fails + 1)); else echo "ok   - mutant killed: $name"; fi
}
mut extra-trigger-workflow_call W 's.replace("  workflow_dispatch:\n", "  workflow_call:\n  workflow_dispatch:\n",1)'
mut extra-trigger-repo-dispatch W 's.replace("  workflow_dispatch:\n", "  repository_dispatch:\n  workflow_dispatch:\n",1)'
mut extra-trigger-push W 's.replace("on:\n", "on:\n  push:\n",1)'
mut inline-on W 's.replace("on:\n  workflow_dispatch:", "on: [workflow_dispatch, push]\n_x:",1)'
mut no-persist-false W 's.replace("persist-credentials: false", "persist-credentials: true",1)'
mut idtoken-in-build W 's.replace("    timeout-minutes: 60\n", "    timeout-minutes: 60\n    permissions:\n      id-token: write\n",1)'
mut homelab-runner W 's.replace("runs-on: ubuntu-latest", "runs-on: homelab",1)'
mut object-env W 's.replace("    environment: play-internal\n", "    environment:\n      name: play-internal\n",1)'
mut build-environment W 's.replace("    timeout-minutes: 60\n", "    timeout-minutes: 60\n    environment: play-internal\n",1)'
mut prod-env-upload W 's.replace("environment: play-internal", "environment: play-production",1)'
mut dry-run-default-false W 's.replace("default: true", "default: false",1)'
mut tag-checkout-in-sign W 's.replace("      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n        with:\n          persist-credentials: false\n      - uses: actions/download-artifact", "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n        with:\n          ref: ${{ needs.build.outputs.source_sha }}\n          persist-credentials: false\n      - uses: actions/download-artifact",1)'
mut main-ref-check-removed W 's.replace("\"$REF\" != \"refs/heads/main\"", "false",1)'
mut tag-script-removed W 's.replace("verify-release-tag.sh", "true")'
mut gradle-no-channel W 's.replace("-PinstallChannel=play", "")'
mut gradle-multiplied-code W 's.replace("-PversionCode=\"$VERSION_BASE\"", "-PversionCode=\"$VERSION_CODE\"",1)'
mut unpinned-action W 's.replace("actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "actions/setup-go@v7",1)'
mut gomobile-latest W 's.replace("gomobile@v0.0.0-20260709172247-6129f5bee9d5", "gomobile@latest",1)'
mut concurrency-no-dry-run W 's.replace("play-publish-${{ inputs.dry_run }}", "play-publish",1)'
mut cert-pin-removed W 's.replace("play-upload-cert-sha256.txt", "x.txt")'
mut secrets-used W 's.replace("vars.AZURE_TENANT_ID", "secrets.AZURE_TENANT_ID",1)'
mut allow-unsigned-removed W 's.replace("--allow-unsigned", "")'
mut sign-checkout-tag-by-name W 's.replace("        with:\n          persist-credentials: false\n      - uses: actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333 # v8.0.2\n        with:\n          name: play-aab-unsigned", "        with:\n          ref: v${{ needs.build.outputs.version }}\n          persist-credentials: false\n      - uses: actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333 # v8.0.2\n        with:\n          name: play-aab-unsigned",1)'
mut upload-runs-artifact-script W 's.replace("run: bash scripts/ci/play-upload.sh", "run: bash \"$RUNNER_TEMP/in/up.sh\"",1)'
mut permissions-write-all W 's.replace("permissions:\n  contents: read\nconcurrency", "permissions: write-all\nconcurrency",1)'
mut permissions-extra-write W 's.replace("permissions:\n  contents: read\nconcurrency", "permissions:\n  contents: write\nconcurrency",1)'
mut release-cert-compare-disabled W 's.replace("if [ -n \"$signer\" ] && grep", "if false && grep",1)'
mut pin-precheck-neutered W 's.replace("        run: bash scripts/ci/check-play-pins.sh\n", "        run: \"true\"\n",1)'
mut build-precheck-hard-fails-dry-run W 's.replace("check-play-pins.sh --warn-only", "check-play-pins.sh",1)'
mut build-precheck-removed W 's.replace("Pin pre-check (warn only on a dry run)", "Pin note",1)'
mut sign-pin-message-loses-owner-command W 's.replace("Owner: run \x27bash scripts/ci/set-play-upload-pin.sh <upload.keystore>\x27 (runbook step E), merge", "merge",1)'
mut sign-check-without-cert-pin W 's.replace(" --expect-cert-sha256 \"$pin\")\"", ")\"",1)'
mut retention-1-day W 's.replace("retention-days: 7", "retention-days: 1")'
mut release-dispatch-on-tag R 's.replace("gh workflow run play-publish.yml --ref main", "gh workflow run play-publish.yml --ref \"$TAG\"",1)'
mut release-dispatch-ungated R "s.replace(\"if: \${{ vars.PLAY_PUBLISH_ENABLED == 'true' }}\", \"\",1)"
mut verify-ancestor-removed V 's.replace("git merge-base --is-ancestor", "true",1)'
mut verify-regex-removed V 's.replace("^[0-9]+\\.[0-9]+\\.[0-9]+$", "^.*$",1)'
[ "$fails" = 0 ]
