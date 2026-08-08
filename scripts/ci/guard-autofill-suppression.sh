#!/usr/bin/env bash
#
# Guard: the central browser-autofill/field-history suppression (ut-docs#400)
# stays wired up on EVERY standalone document, not just the ones that go
# through web/ui/layouts/base.html.
#
# Why a per-document guard, not just "does app.js still have the code": most
# pages render via base.html, which loads every page's shared scripts —
# app.js among them. But four page templates are whole standalone documents
# that bypass that layout and are responsible for their own <script> tags:
# web/ui/pages/login.html, setup.html, self_order.html and
# self_order_shop.html. That is the EXACT same shape of gap ut-docs#344 hit
# with htmx (setup.html silently missing the htmx script tag broke join
# enrolment in the field) — which is why guard-htmx-loaded.sh exists and
# this guard is modelled on it directly, including scoping to standalone
# documents the same way. A first version of this guard checked only that
# base.html loads the sweep and missed all four of these — including
# login.html, the screen every operator handover on a SHARED till shows,
# exactly the threat model ut-docs#400 itself is about.
#
# Two invariants:
#   1. Every standalone document (contains <html>, so it owns its own
#      <script> tags) loads web/public/autofill.js.
#   2. web/public/autofill.js still contains the sweep's own machinery (the
#      MutationObserver, the eligible-input-types table, the
#      data-allow-autofill escape hatch, the htmx:afterSwap listener) — a
#      refactor could plausibly delete or rename the whole IIFE without
#      breaking any Go test, since none of this is exercised by
#      `go test ./...`.
#
# This is a static presence check, not a behavioural one — e2e/tests/
# autofill-suppression-400.spec.ts covers the actual runtime behaviour (real
# Chromium, real rendered attributes, including a /login case). The two are
# complementary: this guard is cheap and catches "the mechanism was deleted
# or a document silently stopped loading it"; the e2e spec catches "the
# mechanism is present but wrong".
#
# Explicit file arguments run this guard against fixtures instead of the
# real tree (see guard-autofill-suppression_test.sh) — same convention as
# guard-htmx-loaded.sh: the first argument is a UI directory to scan for
# standalone documents, the second is the autofill.js path to check.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UI_DIR="${ROOT_DIR}/web/ui"
AUTOFILL_JS="${ROOT_DIR}/web/public/autofill.js"

if [ "$#" -ge 1 ]; then UI_DIR="$1"; fi
if [ "$#" -ge 2 ]; then AUTOFILL_JS="$2"; fi

if [ ! -d "$UI_DIR" ]; then
  echo "❌ autofill guard: ${UI_DIR} does not exist" >&2
  exit 1
fi
if [ ! -f "$AUTOFILL_JS" ]; then
  echo "❌ autofill guard: ${AUTOFILL_JS} does not exist" >&2
  exit 1
fi

failed=0
checked=0

# Every check runs against files with HTML/JS comments stripped first.
# Matching raw text let a COMMENTED-OUT script tag satisfy the rule in an
# earlier guard here (guard-htmx-loaded.sh's own history) — someone
# disabling a script to debug a load-order problem would otherwise have kept
# CI green while silently reintroducing the gap.
strip_html_comments() { perl -0777 -pe 's/<!--.*?-->//gs' "$1"; }
strip_js_comments() { perl -0777 -pe 's{//[^\n]*}{}g; s{/\*.*?\*/}{}gs' "$1"; }

while IFS= read -r -d '' tpl; do
  stripped="$(strip_html_comments "$tpl")"
  # Whole documents only: a partial has no <html> and inherits from its
  # layout (web/ui/layouts/base.html), which is checked as one of these
  # documents in its own right.
  printf '%s' "$stripped" | grep -qE '^[[:space:]]*(<!DOCTYPE|<html\b)' || continue

  checked=$((checked + 1))

  if ! printf '%s' "$stripped" | grep -qE '<script[^>]*src="[^"]*\bautofill\.js'; then
    rel="${tpl#"${ROOT_DIR}/"}"
    echo "❌ autofill guard: ${rel} is a standalone document but never loads" >&2
    echo "   autofill.js — the ut-docs#400 suppression sweep would never run" >&2
    echo "   on this page, silently reintroducing the browser's field-history" >&2
    echo "   dropdown. Fix: add the <script> tag (see web/ui/layouts/base.html" >&2
    echo "   or web/ui/pages/login.html)." >&2
    failed=1
  fi
done < <(find "$UI_DIR" -name '*.html' -print0)

if [ "$checked" -eq 0 ]; then
  # Fail closed: if this ever finds nothing to check, the templates moved or
  # the <html> detection drifted, and the guard is no longer guarding.
  echo "❌ autofill guard: no standalone document was found under ${UI_DIR#"${ROOT_DIR}/"}." >&2
  echo "   Expected several (e.g. web/ui/layouts/base.html, login.html)." >&2
  exit 1
fi

stripped_js="$(strip_js_comments "$AUTOFILL_JS")"
for marker in 'MutationObserver' 'TEXTY_TYPES' 'data-allow-autofill' 'htmx:afterSwap'; do
  if ! printf '%s' "$stripped_js" | grep -qF -- "$marker"; then
    echo "❌ autofill guard: ${AUTOFILL_JS} is missing '${marker}' (outside" >&2
    echo "   comments) — part of the ut-docs#400 suppression sweep looks to" >&2
    echo "   have been removed or renamed. See" >&2
    echo "   e2e/tests/autofill-suppression-400.spec.ts for what actual" >&2
    echo "   behaviour this protects." >&2
    failed=1
  fi
done

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "✓ autofill guard: ${checked} standalone document(s) all load autofill.js, and its sweep is intact"
