#!/usr/bin/env bash
#
# ADR-0113 §1.6 (ut-docs#2795): the public-demo till must never open an
# outbound connection, so every outbound HTTP client and raw TCP dial in the
# till is built by internal/netaccess, whose clients/dialers deny everything
# while demo mode is on. This guard is the mechanical backstop, in the shape
# of guard-data-access.sh: a grep, not a type-system enforcement.
#
# Forbidden in Go code under internal/, outside internal/netaccess:
#   - `http.Client{`  — any http.Client composite literal (`&http.Client{` and
#                        the value form alike); use netaccess.NewClient /
#                        netaccess.NewClientWithTransport.
#   - `net.Dial`      — net.Dial, net.DialTimeout, net.DialTCP/UDP/IP/Unix and
#                        net.Dialer; use netaccess.DialContext.
#
# Scope and exemptions, and why:
#   - internal/ only. cmd/ (the desktop shell) and mobile/ only probe the
#     till's own embedded loopback server and never run on a demo till (the
#     demo runs the plain server binary, ADR-0113 §2); scripts/ and e2e/ are
#     developer/CI tooling, the same carve-out guard-data-access.sh makes.
#   - *_test.go: tests build their own clients against httptest servers.
#   - Full-line comments: prose naming net.Dialer is not a dial.
#   - A reviewed exception carries a same-line
#     `// netaccess:allow <reason>` comment (the reason is required). Used
#     only where no packet leaves the host at all (a UDP route lookup, a
#     loopback probe of the till's own listener).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

pattern='http\.Client\{|net\.Dial'

matches="$(grep -rnE "${pattern}" --include='*.go' internal 2>/dev/null \
  | grep -v '_test\.go:' \
  | grep -v '^internal/netaccess/' \
  | grep -vE '^[^:]+:[0-9]+:[[:space:]]*//' \
  | grep -vE 'netaccess:allow[[:space:]]+[^[:space:]]' || true)"

if [[ -n "${matches}" ]]; then
  echo "❌ netaccess guard: outbound HTTP client or raw dial built outside internal/netaccess" >&2
  echo "   (ADR-0113 §1.6 — use netaccess.NewClient / NewClientWithTransport / DialContext," >&2
  echo "   so the public demo till can deny it; a reviewed exception that sends nothing off-host" >&2
  echo "   takes a same-line '// netaccess:allow <reason>' comment)" >&2
  echo "${matches}" >&2
  exit 1
fi

echo "✓ netaccess guard: every outbound client and dial goes through internal/netaccess"
