#!/usr/bin/env bash
#
# Wiring guard for .github/workflows/app-store-submit.yml (ut-docs#6413): the
# only workflow that can send a build to App Review, in a PUBLIC repo with the
# App Store Connect key. Pins: workflow_dispatch only; environment
# app-store-review; ubuntu-latest, no self-hosted; dry_run defaults to true;
# every action SHA-pinned; contents: read only; no `${{ secrets.` inside a
# run: block; the key is written 0600 and removed in an always() step; and NO
# store-release:allow marker (the opening card adds it together with
# store-release/apple.opened, ut-docs#6414). Mutation-tested against copies.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
WF=".github/workflows/app-store-submit.yml"
fails=0
t="$(mktemp -d)"; trap 'rm -rf "$t"' EXIT
check() { # check <file> -> prints FAIL lines (comment lines are ignored)
  local f="$t/code.yml"
  grep -vE '^[[:space:]]*#' "$1" >"$f"
  grep -qE '^  workflow_dispatch:' "$f" || echo "FAIL:no workflow_dispatch"
  grep -qE '^  (push|pull_request|pull_request_target|release|schedule|workflow_run|issue_comment):' "$f" && echo "FAIL:forbidden trigger"
  grep -qE '^    environment: app-store-review$' "$f" || echo "FAIL:not in the app-store-review environment"
  grep -qE '^    runs-on: ubuntu-latest$' "$f" || echo "FAIL:not ubuntu-latest"
  grep -qiE 'self-hosted|homelab' "$f" && echo "FAIL:self-hosted runner"
  awk '/^      dry_run:/{f=1} f&&/default:/{print $2; exit}' "$f" | grep -qx 'true' || echo "FAIL:dry_run does not default to true"
  if grep -E '^ *(- )?uses: ' "$f" | grep -vqE 'uses: [^@ ]+@[0-9a-f]{40}( |$)'; then echo "FAIL:an action is not pinned to a SHA"; fi
  grep -qE '^  contents: read$' "$f" || echo "FAIL:permissions not contents: read"
  grep -qE '^  [a-z-]+: write$' "$f" && echo "FAIL:a write permission"
  # shellcheck disable=SC2016
  awk '/^ *run: *\|/{r=1;next} r&&/^      - /{r=0} r' "$f" | grep -qF '${{ secrets.' && echo "FAIL:secrets expanded inside run:"
  grep -qF 'chmod 600' "$f" || echo "FAIL:key not chmod 600"
  awk '/^      - name: /{ if (b ~ /if: always\(\)/ && b ~ /rm -rf/ && b ~ /private_keys/) ok=1; b=$0; next } {b=b "\n" $0} END{ if (b ~ /if: always\(\)/ && b ~ /rm -rf/ && b ~ /private_keys/) ok=1; exit ok?0:1 }' "$f" || echo "FAIL:no always() key removal"
  grep -qE 'store-release:allow' "$f" && echo "FAIL:carries a store-release:allow marker before the owner opens the store"
  grep -qF 'go run ./scripts/asc-submit' "$f" || echo "FAIL:does not run scripts/asc-submit"
  return 0
}
real="$(check "$WF")"
if [ -z "$real" ]; then echo "ok   - ${WF} passes every check"; else echo "$real" >&2; fails=$((fails + 1)); fi
mut() { # mut <name> <sed expr> <expected message>
  sed -E "$2" "$WF" >"$t/m.yml"
  if check "$t/m.yml" | grep -qF "$3"; then echo "ok   - mutation caught: $1"; else echo "FAIL - mutation survived: $1" >&2; fails=$((fails + 1)); fi
}
mut dry-run-default 's/^(        default: )true$/\1false/' 'dry_run does not default'
mut env-removed 's/^    environment: app-store-review$//' 'app-store-review environment'
mut macos 's/^    runs-on: ubuntu-latest$/    runs-on: macos-26/' 'not ubuntu-latest'
mut push-trigger 's/^  workflow_dispatch:$/  push:\n  workflow_dispatch:/' 'forbidden trigger'
mut unpinned 's/actions\/checkout@[0-9a-f]{40}/actions\/checkout@v7/' 'not pinned'
mut marker 's/^name: app-store-submit$/name: app-store-submit # store-release:allow ut-docs#6414/' 'store-release:allow marker'
mut write-perm 's/^  contents: read$/  contents: write/' 'write permission'
[ "$fails" -eq 0 ] || { echo "${fails} check(s) failed" >&2; exit 1; }
