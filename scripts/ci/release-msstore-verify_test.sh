#!/usr/bin/env bash
# Wiring test: release.yml dispatches the Microsoft Store verify after the
# installer blob is published (ut-docs#6407). Warn-only, verify_only, least privilege.
# shellcheck disable=SC2015,SC2016
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"
WF=.github/workflows/release.yml
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }
has() { grep -qF -- "$2" <<<"$1" && ok "$3" || bad "$3"; }
hasnt() { ! grep -qF -- "$2" <<<"$1" && ok "$3" || bad "$3"; }
j="$(awk -v j="  msstore-verify-dispatch:" '$0==j{on=1; print; next} on && /^  [a-z][a-z0-9-]*:$/{exit} on{print}' "$WF")"
[ -n "$j" ] && ok "job msstore-verify-dispatch exists" || { bad "job missing"; exit 1; }
has "$j" 'needs: [prepare, publish-installer]' "waits for the installer blob (publish-installer), not only publish-release"
has "$j" "needs.publish-installer.result == 'success'" "runs only when publish-installer succeeded"
has "$j" '!cancelled()' "not skipped by a non-gating sibling"
has "$j" 'runs-on: ubuntu-latest' "GitHub-hosted runner"
has "$j" 'timeout-minutes: 5' "bounded"
perm="$(awk '/^    permissions:/{on=1;next} on && /^    [a-z]/{exit} on{print}' <<<"$j" | sed 's/ *#.*//' | tr -d ' \n')"
[ "$perm" = "actions:write" ] && ok "job permissions are exactly actions: write" || bad "permissions: $perm"
has "$j" 'gh workflow run msstore-submit.yml' "dispatches msstore-submit.yml"
has "$j" '--ref main' "from main (the file carries verify_only)"
has "$j" '-f verify_only=true' "verify_only=true: no Store call, no approval"
has "$j" '-f dry_run=true' "dry_run=true"
has "$j" '-f commit=false' "commit=false"
has "$j" '|| echo "::warning::' "failure only warns (never fails or rolls back the release)"
hasnt "$j" 'continue-on-error' "no continue-on-error needed"
hasnt "$j" '${{ needs.prepare.outputs.tag }}"' "tag reaches the script through env, not inline"
! grep -qE 'run:.*\$\{\{' <<<"$j" && ok "no expression inside run:" || bad "expression inside run:"
[ "$fails" = 0 ] || { echo "release-msstore-verify_test: $fails failure(s)" >&2; exit 1; }
echo "✓ release-msstore-verify_test: all cases passed"
