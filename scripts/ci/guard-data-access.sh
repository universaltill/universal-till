#!/usr/bin/env bash
#
# Enforces the repository pattern (docs reference/coding-standards.md §1):
# raw SQL must live in the data layer (internal/data) or migrations (internal/db).
# Fails if a SQL statement appears in a Go source file anywhere else.
#
# database/sql *types* (*sql.Tx, sql.ErrNoRows, ...) are fine outside the data
# layer — transaction handles are threaded through the domain layer. This guard
# only rejects actual SQL query text.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# SQL statement as a Go string literal on one line (…"SELECT / `INSERT INTO …),
# or the start of a multi-line backtick query (a line beginning with a keyword).
same_line='("|`)[[:space:]]*(SELECT|INSERT[[:space:]]+INTO|UPDATE[[:space:]]|DELETE[[:space:]]+FROM|CREATE[[:space:]]+TABLE)'
line_start='^[[:space:]]*(SELECT[[:space:]]|INSERT[[:space:]]+INTO[[:space:]]|UPDATE[[:space:]]+[A-Za-z_]+[[:space:]]+SET|DELETE[[:space:]]+FROM[[:space:]]|CREATE[[:space:]]+TABLE[[:space:]])'

matches="$(grep -rnE "${same_line}|${line_start}" --include='*.go' internal 2>/dev/null \
  | grep -vE '/(data|db|testsupport)/' \
  | grep -v '_test.go' || true)"

if [[ -n "${matches}" ]]; then
  echo "❌ data-access guard: inline SQL found outside internal/data and internal/db" >&2
  echo "   (repository pattern — move the query into a repository method in internal/data)" >&2
  echo "${matches}" >&2
  exit 1
fi

echo "✓ data-access guard: no inline SQL outside internal/data / internal/db"
