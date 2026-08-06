#!/usr/bin/env bash
#
# Every standalone page template that uses hx-* attributes must actually load
# htmx.min.js.
#
# Why (ut-docs#344): web/ui/pages/setup.html carried
# `hx-post="/api/setup/join"` but loaded only alpine.min.js and cursor.js.
# Without htmx those attributes are inert markup, and that form has no
# action/method fallback — so pressing "Join" issued a plain GET back to the
# setup page. No request ever reached POST /api/setup/join and the
# #setup-join-msg live region never filled: the user saw the button do nothing.
# Multi-till enrolment (ADR-0011 D2) was impossible on a fresh install, which
# is exactly how it was found — a real Pi-to-Pi setup in the field, 2026-08-06,
# not by any test.
#
# It went unnoticed because nothing connected "uses hx-*" to "loads htmx"; the
# page was spotted only by a human hand-sweeping templates during an unrelated
# review. A test pinned to setup.html alone would not catch the next one, so
# this guard checks the invariant across every template.
#
# Scope: only templates that are a whole document (they contain <html), since
# those are responsible for their own <script> tags. Partials and pages
# rendered inside web/ui/layouts/base.html inherit htmx from the layout and are
# correctly exempt — a partial is swapped INTO a document that already loaded
# it.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UI_DIR="${ROOT_DIR}/web/ui"

# Match hx-* generically, NOT an allowlist. An independent review broke the
# first version with a page whose only htmx usage was hx-on= — the repo
# already uses hx-on 23x, hx-vals 19x, hx-push-url, hx-params, hx-encoding and
# hx-swap-oob, and an enumerated list will always trail the ones in use.
HX_ATTR='(^|[[:space:]])(data-)?hx-[a-z-]+='

# Explicit file arguments scan just those files (this is how the guard itself
# gets tested against adversarial fixtures — an untestable guard is how the
# first version shipped with three separate vacuous-pass holes). No arguments
# scans the repo's own templates, which is what CI runs.
FIXTURE_MODE=0
if [ "$#" -gt 0 ]; then
  FIXTURE_MODE=1
fi

failed=0
checked=0

list_templates() {
  if [ "$FIXTURE_MODE" -eq 1 ]; then
    for f in "$@"; do printf '%s\0' "$f"; done
  else
    find "$UI_DIR" -name '*.html' -print0
  fi
}

while IFS= read -r -d '' tpl; do
  # Every check runs against the file with HTML comments removed. Commented-out
  # markup is not markup: matching the raw text let a COMMENTED-OUT htmx script
  # tag satisfy the rule, so someone disabling it to debug a load-order problem
  # would have kept CI green and silently reintroduced ut-docs#344.
  stripped="$(perl -0777 -pe 's/<!--.*?-->//gs' "$tpl")"

  # Whole documents only: a partial has no <html> and inherits from its layout.
  # Anchored, not a substring search: a partial whose comment merely mentions
  # "<html" must not be misclassified as standalone — that fails CI with no
  # sane fix available, since you cannot add htmx to a partial.
  printf '%s' "$stripped" | grep -qE '^[[:space:]]*(<!DOCTYPE|<html\b)' || continue

  printf '%s' "$stripped" | grep -qE "$HX_ATTR" || continue

  checked=$((checked + 1))

  if ! printf '%s' "$stripped" | grep -qE '<script[^>]*src="[^"]*htmx\.min\.js'; then
    rel="${tpl#"${ROOT_DIR}/"}"
    echo "❌ htmx guard: ${rel} uses hx-* attributes but never loads htmx.min.js" >&2
    echo "   Those attributes are inert without it. If the element also has no" >&2
    echo "   action/method fallback, the control silently does nothing —" >&2
    echo "   see ut-docs#344, where this broke joining an existing shop." >&2
    echo "   Fix: add the htmx.min.js <script> tag (see setup.html), or remove" >&2
    echo "   the misleading hx-* attributes if a plain submit is intended." >&2
    failed=1
  fi
done < <(list_templates "$@")

if [ "$failed" -ne 0 ]; then
  exit 1
fi

if [ "$checked" -eq 0 ] && [ "$FIXTURE_MODE" -eq 0 ]; then
  # Fail closed: if this ever finds nothing to check, the templates moved or
  # the attribute pattern drifted, and the guard is no longer guarding.
  echo "❌ htmx guard: no standalone template using hx-* attributes was found." >&2
  echo "   Expected at least one (e.g. web/ui/pages/setup.html). Either the" >&2
  echo "   templates moved out of ${UI_DIR#"${ROOT_DIR}/"}, or HX_ATTR no longer matches." >&2
  exit 1
fi

echo "✓ htmx guard: ${checked} standalone template(s) using hx-* all load htmx.min.js"
