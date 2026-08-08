#!/usr/bin/env bash
#
# ut-docs#478/#489: Manager.Reload reassigns Pm.Installed, Pm.MenuPlugins and
# (via ReloadPlugins) Deps.Menu inside one critical section under
# common.Deps.PluginMu — see the doc comment on PluginMu in
# internal/pages/common/deps.go. Since ut-docs#460 that section fires
# routinely from a background sync-pull goroutine every 30s, not just from
# HTTP handlers, so an unlocked concurrent read of any of the three isn't
# just stale data — it's a fatal Go "concurrent map read and write" crash.
# ut-docs#478 added locked accessors (MenuSnapshot/InstalledPlugin/
# MenuPluginByKey) and swept every read site under internal/pages onto them;
# this guard is the mechanical backstop stopping that invariant from quietly
# regressing, in the spirit of guard-data-access.sh/guard-kiosk-engine.sh.
#
# Scope: any Go file under internal/pages/**, excluding
# internal/pages/common/deps.go itself (the accessors' own implementation)
# and test files (which exercise the locked accessors under controlled
# single/multi-goroutine conditions and are the test subject, not a
# regression risk). If common.Deps.Manager/plugins.Manager ever grows
# another field reassigned inside Reload's critical section, add its
# pattern below — this is the natural place a future author is pointed to.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

SEARCH_DIR="internal/pages"
EXCLUDE_FILE="internal/pages/common/deps.go"

# Receiver-name-agnostic (this package is not consistent about calling the
# *common.Deps variable "d" — registerShiftsAPI/registerInventoryAPI/
# registerPluginStore use "dp"/"deps", per guard-kiosk-engine.sh's own
# lesson from independent review), one pattern per field Reload reassigns:
#   <ident>.Pm.Installed[     — Pm.Installed key/index access
#   <ident>.Pm.MenuPlugins[   — Pm.MenuPlugins key/index access
#   <ident>.Menu              — Menu itself (word-boundaried, so MenuSnapshot/
#                                BaseMenu/MenuItem never match — no literal
#                                ".Menu" substring precedes those names)
pattern='[A-Za-z_][A-Za-z0-9_]*\.Pm\.Installed\[|[A-Za-z_][A-Za-z0-9_]*\.Pm\.MenuPlugins\[|[A-Za-z_][A-Za-z0-9_]*\.Menu\b'

files="$(grep -rlE "${pattern}" --include='*.go' "${SEARCH_DIR}" 2>/dev/null \
  | grep -v '_test\.go$' \
  | grep -vF "${EXCLUDE_FILE}" || true)"

violations=""
for f in ${files}; do
  # Strip full-line comments so prose that merely mentions these fields
  # (the kind of explanatory comment deps.go itself carries) never
  # false-positives — only actual code references count.
  hits="$(grep -vE '^[[:space:]]*//' "${f}" | grep -nE "${pattern}" || true)"
  if [[ -n "${hits}" ]]; then
    violations+=$'\n'"${f}:"$'\n'"${hits}"$'\n'
  fi
done

if [[ -n "${violations}" ]]; then
  echo "❌ plugin-menu-read guard: unlocked read of Pm.Installed / Pm.MenuPlugins / Menu under internal/pages" >&2
  echo "   (Manager.Reload reassigns these inside PluginMu's critical section — an unlocked concurrent" >&2
  echo "   read is a fatal crash, not just stale data. Use Deps.MenuSnapshot() / InstalledPlugin(id) /" >&2
  echo "   MenuPluginByKey(key) instead — see internal/pages/common/deps.go, ut-docs#478/#489.)" >&2
  echo "${violations}" >&2
  exit 1
fi

echo "✓ plugin-menu-read guard: no unlocked read of Pm.Installed / Pm.MenuPlugins / Menu under internal/pages"
