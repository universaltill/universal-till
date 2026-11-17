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
[ "$fails" = 0 ]
