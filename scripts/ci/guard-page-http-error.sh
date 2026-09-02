#!/usr/bin/env bash
#
# Page-route bare-http.Error guard (ut-docs#1455): a handler registered for
# a user-facing GET page route must never answer with a raw
# http.Error(w, "...", status) — on a pinned Android kiosk that replaces the
# entire WebView document with plain, untranslated text and no way back
# (the kiosk hides Android's own navigation bar), which is exactly the
# "failed to load tables" incident this card reports live. Use
# httpx.RenderError instead (internal/httpx/render_error.go) — it renders
# the same translated, full-layout error page (rail + "Back to sale") every
# other page uses.
#
# Unlike guard-kiosk-engine.sh's plain grep, this reimplements enough of
# routecoverage.go's route classification (go/ast, not text matching) that
# a route pattern merely mentioned in a comment or string never
# false-positives, and — the harder case — the guard follows a handler
# registered via a same-package factory call (e.g. plugins_store_page.go's
# `mux.HandleFunc("/plugins/store", PluginStoreHandler(deps))`), not just an
# inline closure. See scripts/ci/checkpagehttperror/main.go's own doc
# comment for exactly what is and isn't covered (deliberately NOT a helper
# function a handler merely calls — that needs real call-graph analysis).
#
# A reviewed, deliberate exception (a page that intentionally has no
# operator base layout to render into — the pre-enrollment setup wizard,
# the anonymous customer order-tracking page) carries a same-line
# "// page-error:allow <reason>" comment, the same escape-hatch convention
# guard-i18n.sh's "i18n:ignore" and guard-compliance-claims.sh's
# "compliance-claim:allow" already use.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

go run ./scripts/ci/checkpagehttperror internal/pages
