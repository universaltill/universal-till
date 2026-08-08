#!/usr/bin/env bash
#
# ut-docs#450: the self-order kiosk's basket engine (common.Deps.KioskEngine)
# was split from the cashier's (common.Deps.Engine) by ut-docs#449, because an
# anonymous, auth-exempt /api/self-order/* request must never read or mutate
# the cashier's live sale. That split is enforced by developer discipline
# only — nothing stops a future kiosk-facing handler from silently reaching
# d.Engine again and reintroducing the exact bug #449 fixed. This guard is
# the mechanical backstop, in the spirit of guard-data-access.sh: a grep, not
# a type-system enforcement.
#
# Scope: any Go file that registers a route under /self-order or
# /api/self-order/* via mux.HandleFunc(...) must never reference a
# `*common.Deps` field named `Engine` (i.e. `<anything>.Engine`, whatever the
# receiver variable is called — this package is NOT consistent about that:
# most page-registration functions use `d`, but registerShiftsAPI/
# registerInventoryAPI/registerPluginStore use `dp`/`deps`) as CODE —
# comment-only lines don't count, so existing prose like "KioskEngine, not
# Engine (ut-docs#449)" never trips this. A genuine, deliberate exception can
# still reference it on a line carrying an inline
# "kiosk-engine-guard:allow <reason>" comment.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

SEARCH_DIR="internal/pages"

# Files that register at least one /self-order or /api/self-order/* route.
# The leading "METHOD " token (Go 1.22+ mux pattern) is optional — this
# package uses BOTH styles (e.g. self_order_shop.go's own
# `mux.HandleFunc("GET /self-order/shop", ...)` vs. the equally common
# bare-path `mux.HandleFunc("/api/pos/scan", ...)` already used elsewhere in
# this exact package, e.g. pos_api.go/catalog/handlers.go) — a kiosk route
# registered bare-path must be detected just the same. Not scoped to a
# self_order_*.go filename convention — the card explicitly asks for "any
# future file serving /self-order or /api/self-order/*", not just today's two.
kiosk_route_files="$(grep -rlE 'mux\.HandleFunc\("([A-Z]+ )?/(self-order|api/self-order)(/|")' \
  --include='*.go' "${SEARCH_DIR}" 2>/dev/null | grep -v '_test\.go$' || true)"

if [[ -z "${kiosk_route_files}" ]]; then
  echo "❌ kiosk-engine guard: found no file registering a /self-order or /api/self-order route" >&2
  echo "   (registerSelfOrder/registerSelfOrderShop moved, renamed, or the route pattern changed —" >&2
  echo "   update this guard's route-detection regex rather than let it go silently blind)" >&2
  exit 1
fi

# Within each kiosk-route file: strip full-line comments (so prose that
# merely mentions "d.Engine", like the ut-docs#449 explanatory comments
# already in self_order_page.go, never false-positives), then look for any
# `<identifier>.Engine` reference as code — receiver-name-agnostic, since
# hardcoding `d` alone missed the `dp`/`deps` receiver names already used
# elsewhere in this package. `\.Engine` requires the dot immediately before
# "Engine", so this can never match `.KioskEngine` (there's no dot directly
# before "Engine" in that name). A hit is allowed only if the same source
# line carries an explicit "kiosk-engine-guard:allow <reason>" comment.
violations=""
for f in ${kiosk_route_files}; do
  hits="$(grep -vE '^[[:space:]]*//' "${f}" \
    | grep -nE '[A-Za-z_][A-Za-z0-9_]*\.Engine([^A-Za-z0-9_]|$)' \
    | grep -v 'kiosk-engine-guard:allow' || true)"
  if [[ -n "${hits}" ]]; then
    violations+=$'\n'"${f}:"$'\n'"${hits}"$'\n'
  fi
done

if [[ -n "${violations}" ]]; then
  echo "❌ kiosk-engine guard: a self-order route handler references the cashier's Engine directly" >&2
  echo "   (kiosk handlers must use the KioskEngine field instead — see ut-docs#449 / ut-docs#450)" >&2
  echo "   If this is a deliberate, reviewed exception, add an inline" >&2
  echo "   '// kiosk-engine-guard:allow <reason>' comment on that line." >&2
  echo "${violations}" >&2
  exit 1
fi

echo "✓ kiosk-engine guard: no self-order route handler references the cashier's Engine"
