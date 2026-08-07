#!/usr/bin/env bash
#
# Help-topic registry guard (ut-docs#361): a duplicate/conflicting `routes:`
# entry across two web/help/<locale>/*.md topics, or any topic whose front
# matter doesn't parse, doesn't fail loudly on its own AT RUNTIME —
# internal/manual's Builtin() (the loader every "?" link resolves through)
# swallows that error into a log line and silently degrades every
# contextual help link to the generic /help index, on purpose (a broken
# manual must never take the till down mid-sale). That resilience is
# correct at runtime and this guard does not change it.
#
# go test ./... already catches this bug class too (internal/pages'
# Library() loader propagates the same error, and existing tests
# TestEveryTopicResolves/TestManualIsTranslatedInEveryShippedLocale/
# TestRouteRegistryResolvesKnownPages already fail on it) — so this is not
# the only thing standing between bad content and a merge. What it adds:
# one dedicated, clearly-named CI step with a message that states the
# actual root cause directly, instead of the same information arriving as
# several confusingly-worded incidental test failures buried in `go test`
# output (see ut-docs#361's own repro for exactly that confusion) — and it
# runs earlier in the job, before Build/Test.
#
# Unlike its bash/python siblings (e.g. guard-i18n.sh's own JSON key-diffing),
# this guard does not reimplement the front-matter grammar or the
# route-conflict rule (a route is checked against one map shared across ALL
# locales, not per-locale) in shell — that logic is subtle enough that a
# reimplementation risks silently drifting from the real algorithm, which is
# exactly the bug class this guard exists to close. It calls the real
# internal/manual package instead, via scripts/ci/checkhelptopics.
#
# Covers:
#   1. No duplicate/conflicting route across topics.
#   2. Every topic's front matter parses.
#   3. Every shipped locale has translated every topic English has.
#   4. Every user-facing GET page route registered under internal/pages/**
#      (mux.HandleFunc/mux.Handle, found via a go/ast scan) is claimed by
#      some topic's routes: front matter — exactly or via a {param} pattern,
#      matched with the SAME segment-wise matcher the runtime "?" resolves
#      through (manual.RouteCovered), so the guard can't drift from the app.
#      Non-page namespaces (/api/, /ui/ fragments, static assets, …) are
#      denylisted by prefix in routecoverage.go, each with its reason.
#      (Named routecoverage.go, not coverage.go — .gitignore's coverage.*
#      rule for test profiles would swallow that filename.)
#      (ut-docs#326 — this closes the page-route coverage gap that was
#      tracked as ut-docs#365.)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

go run ./scripts/ci/checkhelptopics
