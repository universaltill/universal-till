#!/usr/bin/env bash
#
# Page-route bare-error guard (ut-docs#1455): a raw http.Error(...) call
# (or its internal/pages/common wrappers LocalizedError/LogAndLocalizedError,
# both http.Error under the hood — see internal/pages/common/errors.go)
# reached from inside a top-level page GET route's handler replaces the
# ENTIRE WebView document with a bare-text body: no nav rail, no way back
# on the pinned Android kiosk (no browser Back). Reported live against
# /tables (internal/pages/tables_page.go) — ListTablesWithState failed and
# the WebView showed nothing but "failed to load tables".
#
# The fix is httpx.RenderError (internal/httpx/error_page.go): it renders
# the SAME full layout (base.html + nav) any normal page does, so the nav
# rail and a "Back to sale" link are always reachable even on a failed
# page load. This guard is the mechanical backstop that stops a future
# page handler from silently reintroducing the bare-body pattern.
#
# Like guard-help-topics.sh, this does not reimplement route parsing in
# shell — it calls the real go/ast scan via scripts/ci/checkpagehttperror,
# which mirrors scripts/ci/checkhelptopics/routecoverage.go's own approach
# and non-page-route denylist.
#
# Scope: a "page route" is a mux.HandleFunc/mux.Handle registration whose
# pattern carries no method prefix other than an optional "GET ", under a
# path not in the same non-page denylist (/api/, /ui/, static assets, …)
# routecoverage.go already uses. Only one level of indirection is followed
# — a local, `:=`-assigned closure the page-route handler calls by name
# (see checkpagehttperror/main.go's own doc comment for the exact,
# deliberately-not-expanded boundary).
#
# A flagged line carrying a same-line "// page-error:allow <reason>"
# comment is allowed — same escape-hatch convention as i18n:ignore /
# kiosk-engine-guard:allow elsewhere in this repo. As of ut-docs#1455, the
# ~40 pre-existing page-route call sites this card's own grooming pass
# deliberately left for the follow-up card (ut-docs#1458) carry exactly
# that annotation, citing #1458 — a reviewed, tracked exception, not a
# silently-reintroduced gap.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

go run ./scripts/ci/checkpagehttperror
