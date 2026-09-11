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
#   <ident>.MenuAmendments    — the ADR-0088 layout amendments in force
#                                (reassigned beside Menu; MenuAmendmentsSnapshot
#                                is the accessor — no boundary after "…Amendments"
#                                there, so it never matches)
#   <ident>.ItemsAmendments   — ut-docs#1911: MenuAmendments' Items-slot twin,
#                                reassigned in the same ReloadPlugins critical
#                                section beside MenuAmendments; ItemsAmendmentsSnapshot
#                                is the accessor
#   <ident>.RailAmendments    — ut-docs#1912: the Rail-slot twin (nav.html's
#                                primary links), reassigned in the same critical
#                                section; RailAmendmentsSnapshot is the accessor
#                                (also what httpx.RailAmendmentsProvider is wired
#                                to — that assignment in init.go names the
#                                PROVIDER, "RailAmendmentsProvider", which the \b
#                                boundary keeps from matching, deliberately)
#   <ident>.Pm.LayoutAmendments — the manager's loaded rows (reassigned in
#                                Reload; LayoutAmendmentsSnapshot is the accessor)
#
# NOTE (independent review of ut-docs#1904, F4; extended for ut-docs#1911
# and #1912): the LayoutAmendments pattern only matches a read through a
# *common.Deps ("d.Pm.LayoutAmendments"). It does NOT match a read off a
# *plugins.Manager held directly in a variable —
# common.BuildMenuAmendments/BuildItemsAmendments/BuildRailAmendments all
# take the manager as a parameter and read "pm.LayoutAmendments".
# Widening the pattern to a bare ".LayoutAmendments" would flag those
# functions' own bodies, whose whole contract is "the caller holds
# PluginMu", so the field pattern is deliberately left as-is and the risk is
# guarded where it actually lives instead: at Build*Amendments' CALL SITES,
# below. A new caller of ANY of the three is the regression to catch; the
# existing ones were each checked to hold the lock (or to run at boot,
# before any concurrent reader exists).
pattern='[A-Za-z_][A-Za-z0-9_]*\.Pm\.Installed\[|[A-Za-z_][A-Za-z0-9_]*\.Pm\.MenuPlugins\[|[A-Za-z_][A-Za-z0-9_]*\.Menu\b|[A-Za-z_][A-Za-z0-9_]*\.MenuAmendments\b|[A-Za-z_][A-Za-z0-9_]*\.ItemsAmendments\b|[A-Za-z_][A-Za-z0-9_]*\.RailAmendments\b|[A-Za-z_][A-Za-z0-9_]*\.Pm\.LayoutAmendments\b'

files="$(grep -rlE "${pattern}" --include='*.go' "${SEARCH_DIR}" 2>/dev/null \
  | grep -v '_test\.go$' \
  | grep -vF "${EXCLUDE_FILE}" || true)"

# BuildMenuAmendments/BuildItemsAmendments/BuildRailAmendments all read
# pm.LayoutAmendments and must only ever be called with PluginMu held (or at
# boot, before the 30s sync-pull goroutine that ut-docs#460 introduced
# exists). Their known-safe call sites are allowlisted by file; a NEW one
# fails this guard, so whoever adds it has to come here and say which lock
# they hold. This is the check that actually covers the field, since the
# field pattern above cannot see through a parameter.
CALL_PATTERN='Build(Menu|Items|Rail)Amendments\('
CALL_ALLOWLIST='internal/pages/common/deps.go|internal/pages/common/state.go|internal/pages/init.go'

call_files="$(grep -rlE "${CALL_PATTERN}" --include='*.go' "${SEARCH_DIR}" 2>/dev/null \
  | grep -v '_test\.go$' \
  | grep -vE "^(${CALL_ALLOWLIST})$" || true)"

if [[ -n "${call_files}" ]]; then
  echo "❌ plugin-menu-read guard: BuildMenuAmendments/BuildItemsAmendments/BuildRailAmendments called outside its allowlisted call sites" >&2
  echo "   (it reads pm.LayoutAmendments, which Manager.Reload reassigns under PluginMu — an" >&2
  echo "   unlocked concurrent read is a fatal crash, not stale data. If the new call site does" >&2
  echo "   hold PluginMu, add its file to CALL_ALLOWLIST in this script and say so in a comment." >&2
  echo "   See ADR-0088 and ut-docs#478/#489/#1911/#1912.)" >&2
  echo "${call_files}" >&2
  exit 1
fi

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
  echo "❌ plugin-menu-read guard: unlocked read of Pm.Installed / Pm.MenuPlugins / Menu / MenuAmendments / ItemsAmendments / RailAmendments / Pm.LayoutAmendments under internal/pages" >&2
  echo "   (Manager.Reload reassigns these inside PluginMu's critical section — an unlocked concurrent" >&2
  echo "   read is a fatal crash, not just stale data. Use Deps.MenuSnapshot() / InstalledPlugin(id) /" >&2
  echo "   MenuPluginByKey(key) / MenuAmendmentsSnapshot() / ItemsAmendmentsSnapshot() / RailAmendmentsSnapshot() /" >&2
  echo "   LayoutAmendmentsSnapshot() instead — see internal/pages/common/deps.go, ut-docs#478/#489/#1911/#1912, ADR-0088.)" >&2
  echo "${violations}" >&2
  exit 1
fi

echo "✓ plugin-menu-read guard: no unlocked read of Pm.Installed / Pm.MenuPlugins / Menu / MenuAmendments / ItemsAmendments / RailAmendments / Pm.LayoutAmendments under internal/pages"
