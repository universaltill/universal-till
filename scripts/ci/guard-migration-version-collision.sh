#!/usr/bin/env bash
#
# Enforces unique migration version numbers (ut-docs#1056, found live: two
# concurrent pipeline lanes independently created internal/db/migrations/
# 067_vouchers.sql and 067_shift_cash_reconciliation.sql, each branched
# from a similar point in history).
#
# This is NOT a sort-order/tie-break problem (correction, follow-up
# independent review — an earlier version of this comment claimed it was,
# which was wrong): internal/db/db.go's migrate() reads the highest
# applied version ONCE via `SELECT MAX(version)` before its loop, then
# skips any migration whose Version is <= that watermark — never
# re-checking per file. On an upgrade where an install already has ONE of
# two same-numbered files, a later release's SECOND same-numbered file is
# skipped entirely, forever, silently — the watermark already passed that
# number and never looks backward (this schema is fiscal/compliance-
# relevant, so that's a real data-loss class, not cosmetic). On a fresh
# install both try to apply instead, and the second's own INSERT into
# schema_migrations (version is its PRIMARY KEY) fails loudly — a
# different, but still wrong, outcome. loadMigrations itself now
# hard-fails on a duplicate too (checkNoDuplicateVersions) — this guard is
# the earlier, faster signal at PR time, without needing a Go build.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# Overridable so the regression test can point this at a scratch directory
# instead of planting live fixtures into the real, embedded, append-only
# migrations tree (found in independent review, ut-docs#1056: a leftover
# fixture there — trap missed on SIGKILL/power loss — would get
# //go:embed'd and, since internal/db/db.go applies by a high-watermark
# `version > current` check, permanently skip every real migration above
# it on that install: the exact silent data-loss class this guard exists
# to prevent).
MIGRATIONS_DIR="${MIGRATIONS_DIR:-internal/db/migrations}"

if [[ ! -d "${MIGRATIONS_DIR}" ]]; then
  echo "❌ migration-version-collision guard: ${MIGRATIONS_DIR} does not exist (renamed or missing?)" >&2
  exit 1
fi

versions_file="$(mktemp)"
trap 'rm -f "${versions_file}"' EXIT

unparseable=()
for f in "${MIGRATIONS_DIR}"/*.sql; do
  [[ -e "${f}" ]] || continue
  name="$(basename "${f}")"
  if [[ "${name}" =~ ^([0-9]+)_ ]]; then
    # Normalize with base-10 (10#) so "67" and "067" compare equal — a
    # plain string compare missed this (independent review, ut-docs#1056):
    # `internal/db/migration.go`'s strconv.Atoi parses both as the same
    # int, so the Go loader would catch a zero-padding collision the guard
    # was silently letting through.
    version="$((10#${BASH_REMATCH[1]}))"
    echo "${version} ${name}" >>"${versions_file}"
  else
    unparseable+=("${name}")
  fi
done

if [[ ${#unparseable[@]} -gt 0 ]]; then
  echo "❌ migration-version-collision guard: filename(s) with no parseable leading version number" >&2
  for name in "${unparseable[@]}"; do
    echo "  - ${name}" >&2
  done
  echo "   (internal/db/migration.go's loadMigrations hard-fails on these too — name must start with digits + '_')" >&2
  exit 1
fi

dup_versions="$(cut -d' ' -f1 "${versions_file}" | sort -n | uniq -d)"

if [[ -n "${dup_versions}" ]]; then
  echo "❌ migration-version-collision guard: duplicate migration version number(s) found" >&2
  while read -r v; do
    [[ -n "${v}" ]] || continue
    echo "  version ${v}:" >&2
    grep "^${v} " "${versions_file}" | awk '{print "    - " $2}' >&2
  done <<<"${dup_versions}"
  echo "   (rename one file's leading number so every version is unique — ut-docs#1056)" >&2
  exit 1
fi

echo "✓ migration-version-collision guard: every ${MIGRATIONS_DIR}/*.sql version number is unique"
