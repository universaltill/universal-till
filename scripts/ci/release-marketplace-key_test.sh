#!/usr/bin/env bash
#
# Wiring + behaviour test for ut-docs#4356: release.yml requires the compiled
# marketplace key (ADR-0158) only in unitill-pos, the binary that verifies
# plugins. The desktop shells never read buildinfo.MarketplacePublicKey, so
# Go's linker drops the -X value from them; v0.34.3's verify-versions failed
# on exactly that. (macOS's .app shell only "passed" because build-app.sh
# builds without -trimpath, so Go's embedded build info records the -ldflags
# text, key included — the grep matched that, not a linked variable.)
#
# Pins:
#   - verify-versions calls check() with "key" for every unitill-pos and
#     never for a unitill-desktop;
#   - the .dmg check gates its key test on unitill-pos;
#   - check() itself, extracted from release.yml and run on fixtures: a
#     shell with the version and no key passes; a server without the key
#     fails; a server with it passes;
#   - the shell still never runs server code: it links internal/config,
#     internal/plugins and internal/enroll (readers of the key) only as dead
#     code, so it must not depend on internal/app or internal/pages, and
#     cmd/unitill-desktop must not name the key. If it ever does, the shells
#     need the key check back.
# Readers consume their whole input (here-strings, no `grep -q` on a pipe) so
# pipefail never turns a SIGPIPE into a false "missing" (ut-docs#2941).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
WF=".github/workflows/release.yml"

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

wf="$(cat "$WF")"

# 1. verify-versions call sites.
calls="$(grep -E '^[[:space:]]+check extracted/' <<<"$wf" || true)"
pos_calls="$(grep -E 'check extracted/unitill-pos(\.exe)? ' <<<"$calls" || true)"
shell_calls="$(grep -E 'check extracted/unitill-desktop' <<<"$calls" || true)"
if [ -z "$pos_calls" ] || [ -z "$shell_calls" ]; then
  fail "found no unitill-pos or no unitill-desktop check() call in ${WF} (parser broke?)"
fi
while IFS= read -r c; do
  [ -n "$c" ] || continue
  if [[ "$c" =~ \"[[:space:]]+key[[:space:]]*$ ]]; then
    pass "server check requires the key: ${c#"${c%%[![:space:]]*}"}"
  else
    fail "server check does not require the key: ${c#"${c%%[![:space:]]*}"}"
  fi
done <<<"$pos_calls"
while IFS= read -r c; do
  [ -n "$c" ] || continue
  if [[ "$c" =~ \"[[:space:]]+key[[:space:]]*$ ]]; then
    fail "shell check requires the key the linker drops: ${c#"${c%%[![:space:]]*}"}"
  else
    pass "shell check skips the key: ${c#"${c%%[![:space:]]*}"}"
  fi
done <<<"$shell_calls"

# 2. The .dmg loop.
# The pattern matches the literal "$bin" text in release.yml.
# shellcheck disable=SC2016
if grep -qE 'elif \[ "\$bin" = unitill-pos \] && \{ .*UT_MARKETPLACE_KEY_HEX' <<<"$wf"; then
  pass ".dmg check tests the key on unitill-pos only"
else
  fail ".dmg check's key test is not gated on unitill-pos"
fi

# 3. Behaviour: run the real check() from verify-versions on fixtures.
fn="$(awk '/^          check\(\) \{$/{on=1} on{print} on && /^          \}$/{exit}' "$WF")"
if [ -z "$fn" ]; then
  fail "could not extract check() from ${WF}"
else
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  key="$(printf 'ab%.0s' {1..32})"
  printf 'junk\n0.0.1\nmore\n' >"$tmp/shell"
  printf 'junk\n0.0.1\n%s\n' "$key" >"$tmp/server-with-key"
  printf 'junk\n0.0.1\nmore\n' >"$tmp/server-no-key"
  # NOTE: the unstamped-fixture assertions below document current behaviour
  # (they passed before ut-docs#5205 too); only the literal-grep assertions
  # on "dev" are regression catchers.
  # An unstamped build (ldflags -X broken): the default "dev" is a 3-char run,
  # which `strings` drops, so it never shows as a line and is caught as a
  # missing version (ut-docs#5205).
  printf 'junk\ndev\nmore\n' >"$tmp/unstamped"
  run_check() { # run_check <file> [key] → prints "fail=<n>"
    VERSION=0.0.1 UT_MARKETPLACE_KEY_HEX="$key" bash -c "
      set -euo pipefail
      fail=0
      $fn
      check \"\$1\" label \${2:-} >/dev/null 2>&1
      echo \"fail=\$fail\"" _ "$@"
  }
  expect() { # expect <want> <description> <check args…>
    local want="$1" desc="$2" got
    shift 2
    got="$(run_check "$@")"
    if [ "$got" = "$want" ]; then pass "$desc"; else fail "$desc (got $got)"; fi
  }
  expect fail=0 "a shell with the version and no key passes" "$tmp/shell"
  expect fail=1 "a server without the key fails" "$tmp/server-no-key" key
  expect fail=0 "a server with the key passes" "$tmp/server-with-key" key
  expect fail=1 "an unstamped build (line \"dev\", no version) fails" "$tmp/unstamped"
  unstamped_msg="$(VERSION=0.0.1 bash -c "
    set -euo pipefail
    fail=0
    $fn
    check \"\$1\" label" _ "$tmp/unstamped" 2>&1 || true)"
  if grep -q "does not contain the expected version" <<<"$unstamped_msg"; then
    pass "an unstamped build fails with the expected-version error"
  else
    fail "an unstamped build did not fail with the expected-version error: $unstamped_msg"
  fi
  # The dead branch stays removed (strings -a drops 3-char runs).
  # The pattern is the literal workflow text, not an expansion.
  # shellcheck disable=SC2016
  if grep -qF 'grep -qx "dev" <<< "$out"' <<<"$fn"; then
    fail "check() still greps for a 3-char \"dev\" line that strings never prints"
  else
    pass "check() has no dead \"dev\" branch"
  fi
fi

# Whole-file: no dead 3-char "dev" grep anywhere in release.yml.
# The pattern is the literal workflow text.
# shellcheck disable=SC2016
if grep -qF 'grep -qx "dev"' <<<"$wf"; then
  fail "release.yml still contains grep -qx \"dev\" (strings never prints 3-char runs)"
else
  pass "release.yml contains no dead grep -qx \"dev\""
fi

# 4. The premise: the shell never runs server code or reads the key.
refs="$(grep -rn 'MarketplacePublicKey' cmd/unitill-desktop --include='*.go' || true)"
if [ -z "$refs" ]; then
  pass "cmd/unitill-desktop never names buildinfo.MarketplacePublicKey"
else
  fail "cmd/unitill-desktop now reads the key — give its release checks the key back: $refs"
fi
mod="github.com/universaltill/universal-till"
for goos in linux windows darwin; do
  if ! deps="$(GOOS="$goos" go list -tags desktop -deps ./cmd/unitill-desktop 2>/dev/null)"; then
    # Windows' webview_go is cgo-only, so go list exits non-zero there but
    # still prints the deps it resolved; an empty list is the real failure.
    :
  fi
  if ! grep -qx "${mod}/cmd/unitill-desktop" <<<"$deps"; then
    fail "go list printed no deps for the ${goos} shell (toolchain broke?)"
    continue
  fi
  server="$(grep -xE "${mod}/internal/(app|pages)" <<<"$deps" || true)"
  if [ -z "$server" ]; then
    pass "${goos} shell links no server code (internal/app, internal/pages)"
  else
    fail "${goos} shell now links server code — it may verify plugins, give its release checks the key back: ${server//$'\n'/ }"
  fi
done

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all release marketplace-key checks pass"
