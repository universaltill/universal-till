#!/usr/bin/env bash
#
# Guard: the App Store build never downloads or imports code (ADR-0194, Apple
# guideline 2.5.2; ut-docs#3433, #6565). The first-party Stripe and SumUp
# payment plugins ship inside the app (internal/plugins/bundled) and are only
# switched on and off. This makes that a check, not prose. It fails when:
#
#   gate     an install/import/update/directive entry point lacks its
#            channel-gate call (plugins.RefuseCodeCanonicalType /
#            RefuseCodeRuntime / refuseCodeInstall / codeLoadAllowed), per the
#            table below; or a new file builds a marketplace install request or
#            an Importer without being in that table (a new entry point must be
#            gated and listed here);
#   network  the bundled package imports net/http, net or the marketplace
#            client (bundled code arrives by build, never by network);
#   lock     bundled.lock.json is malformed: not schema 1, a non-first-party
#            id or repo, a version/tag mismatch, or a sha256 that is not 64
#            lowercase hex characters;
#   flag     BundledLockVerified (the one switch that lets a code plugin be
#            persisted on the App Store channel) is set outside
#            internal/plugins/bundled.
#
# Reviewed exception: a same-line `appstore-code-gate:allow <reason>` on the
# line naming the entry point.
#
# Usage: guard-appstore-no-code-fetch.sh [root]  (root defaults to the repo;
# guard-appstore-no-code-fetch_test.sh points it at fixture copies).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ROOT="${1:-${ROOT_DIR}}"
fail=0
report() { echo "❌ appstore-no-code-fetch [$1] $2" >&2; fail=1; }

# file|token that must appear in it (one row per gate site).
GATES='
internal/plugins/installer_marketplace.go|RefuseCodeCanonicalType
internal/plugins/installer_marketplace.go|RefuseCodeRuntime
internal/plugins/installer_store.go|RefuseCodeCanonicalType
internal/plugins/importer.go|RefuseCodeRuntime
internal/plugins/manifest.go|RefuseCodeRuntime
internal/plugins/rollback.go|RefuseCodeRuntime
internal/plugins/update_checker.go|RefuseCodeRuntime
internal/plugins/wasm_runtime.go|codeLoadAllowed
internal/plugins/wasm_compile_warm.go|codeLoadAllowed
internal/plugins/supervisor.go|RefuseCodeRuntime
internal/pages/plugin_api.go|refuseCodeInstall
internal/pages/plugin_api.go|ErrCodePluginRefused
internal/pages/plugins_store_page.go|refuseCodeInstall
internal/pages/cloudsync_wire.go|refuseCodeInstall
internal/buildinfo/channel.go|CodePluginInstallAllowed
'
while IFS='|' read -r file token; do
  [ -n "${file}" ] || continue
  if [ ! -f "${ROOT}/${file}" ]; then
    report gate "${file} is missing"
  elif ! grep -v '^[[:space:]]*//' "${ROOT}/${file}" | grep "${token}" >/dev/null; then
    report gate "${file} has no ${token} call (the App Store build must refuse code plugins here)"
  fi
done <<<"${GATES}"

# Discovery: a non-test file outside internal/plugins that builds an install
# request or an Importer must be one of the gated files above.
gated_files="$(printf '%s\n' "${GATES}" | cut -d'|' -f1 | sort -u)"
while IFS= read -r f; do
  rel="${f#"${ROOT}"/}"
  grep -q "appstore-code-gate:allow" "${f}" && continue
  if ! grep -qxF "${rel}" <<<"${gated_files}"; then
    report gate "${rel} builds a marketplace install request or an Importer but is not in this guard's gate table (gate it with plugins.RefuseCodeCanonicalType / refuseCodeInstall and list it)"
  fi
done < <(
  grep -rlE 'MarketplaceInstallRequest\{|plugins\.NewImporter\(|NewMarketplaceInstaller\(' \
    --include='*.go' "${ROOT}/internal" "${ROOT}/cmd" "${ROOT}/mobile" 2>/dev/null |
    grep -v '_test\.go$' | grep -v "^${ROOT}/internal/plugins/" | LC_ALL=C sort -u
)

# network: bundled imports no network package.
if [ -d "${ROOT}/internal/plugins/bundled" ]; then
  while IFS= read -r f; do
    if grep -nE '^[[:space:]]*(import[[:space:]]+)?([A-Za-z_.]+[[:space:]]+)?"(net/http|net|net/http/httputil|github\.com/universaltill/universal-till/internal/plugins/marketplace)"' "${f}" >/dev/null; then
      report network "${f#"${ROOT}"/} imports a network package; bundled code arrives by build, not by network"
    fi
  done < <(find "${ROOT}/internal/plugins/bundled" -name '*.go' ! -name '*_test.go' | LC_ALL=C sort)
else
  report network "internal/plugins/bundled is missing"
fi

# lock: well-formed.
LOCK="${ROOT}/internal/plugins/bundled/bundled.lock.json"
if [ ! -f "${LOCK}" ]; then
  report lock "bundled.lock.json is missing"
else
  if ! msg="$(LOCK="${LOCK}" python3 -I - <<'PY'
import json, os, re, sys
SHA = re.compile(r"^[0-9a-f]{64}$")
try:
    lock = json.load(open(os.environ["LOCK"]))
except Exception as e:
    print("not valid JSON: %s" % e); sys.exit(1)
errs = []
if lock.get("schema") != 1 or not isinstance(lock.get("plugins"), list) or not lock["plugins"]:
    errs.append("needs schema 1 and a non-empty plugins list")
seen = set()
for e in lock.get("plugins") or []:
    pid = e.get("id", "")
    if not re.match(r"^com\.universaltill\.[a-z0-9][a-z0-9.-]*$", pid) or pid in seen:
        errs.append("bad or repeated id %r (first-party ids only)" % pid)
    seen.add(pid)
    if not re.match(r"^[0-9]+\.[0-9]+\.[0-9]+$", e.get("version", "")):
        errs.append("%s: bad version" % pid)
    if not re.match(r"^universaltill/ut-plugin-[a-z0-9-]+$", e.get("repo", "")):
        errs.append("%s: repo must be universaltill/ut-plugin-*" % pid)
    if e.get("release_tag") != "v" + str(e.get("version")):
        errs.append("%s: release_tag must be v<version>" % pid)
    for k in ("bundle_sha256", "manifest_sha256", "wasm_sha256"):
        if not SHA.match(str(e.get(k, ""))):
            errs.append("%s: %s is not 64 lowercase hex" % (pid, k))
    for rel, h in (e.get("files") or {}).items():
        if not SHA.match(str(h)):
            errs.append("%s: file %s hash is not 64 lowercase hex" % (pid, rel))
if errs:
    print("; ".join(errs)); sys.exit(1)
PY
)"; then
    report lock "bundled.lock.json: ${msg}"
  fi
fi

# flag: BundledLockVerified is set only by the bundled package.
while IFS= read -r f; do
  rel="${f#"${ROOT}"/}"
  case "${rel}" in
    internal/plugins/bundled/*) continue ;;
  esac
  if grep -nE 'BundledLockVerified[[:space:]]*:' "${f}" | grep -v 'appstore-code-gate:allow' >/dev/null; then
    report flag "${rel} sets BundledLockVerified outside internal/plugins/bundled"
  fi
done < <(grep -rl 'BundledLockVerified' --include='*.go' "${ROOT}/internal" "${ROOT}/cmd" "${ROOT}/mobile" 2>/dev/null | grep -v '_test\.go$' | LC_ALL=C sort -u)

if [ "${fail}" -ne 0 ]; then
  echo "   The App Store build never downloads or imports code (ADR-0194, Apple 2.5.2)." >&2
  exit 1
fi
echo "✓ appstore-no-code-fetch guard: every install path is gated, the bundled set has no network, the lock is well-formed"
