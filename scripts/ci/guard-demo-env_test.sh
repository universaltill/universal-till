#!/usr/bin/env bash
#
# Regression test for guard-demo-env.sh (ADR-0113 §1.1, ut-docs#2687):
# proves the guard flags a UT_DEMO / UT_DEMO_TOKEN environment read planted
# outside internal/config/config.go (direct os.Getenv, os.LookupEnv, a local
# getenv helper, the name held in a variable, an os.Environ scan matching
# the "UT_DEMO=" / "UT_DEMO_TOKEN=" prefix), does NOT flag a comment, a
# _test.go file, or a child-process env assignment ("UT_DEMO=1", which the
# demo broker will write), honours the reviewed allow marker, and still
# passes on the real, unmodified codebase.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-demo-env.sh"
FIXTURE_DIR="internal/pages"
OUT="$(mktemp)"
FAIL_COUNT=0

fixtures=()
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  rm -f "${OUT}"
  exit "${status}"
}
trap cleanup EXIT

plant() {
  local file="$1" content="$2"
  local path="${FIXTURE_DIR}/${file}"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

clear_fixtures() {
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  fixtures=()
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >"${OUT}" 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >"${OUT}" 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

plant "zz_guard_demo_env_getenv.go" 'package pages

import "os"

func zzDemo() bool { return os.Getenv("UT_DEMO") == "1" }'
expect_fail "os.Getenv(\"UT_DEMO\") outside internal/config"
clear_fixtures

plant "zz_guard_demo_env_lookup.go" 'package pages

import "os"

func zzDemoToken() string { v, _ := os.LookupEnv("UT_DEMO_TOKEN"); return v }'
expect_fail "os.LookupEnv(\"UT_DEMO_TOKEN\") outside internal/config"
clear_fixtures

plant "zz_guard_demo_env_helper.go" 'package pages

func zzDemo() bool { return getenvZz("UT_DEMO", "0") == "1" }'
expect_fail "a local getenv-style helper reading UT_DEMO"
clear_fixtures

plant "zz_guard_demo_env_const.go" 'package pages

import "os"

const zzDemoKey = "UT_DEMO"

func zzDemo() bool { return os.Getenv(zzDemoKey) == "1" }'
expect_fail "the env var name held in a constant"
clear_fixtures

plant "zz_guard_demo_env_comment.go" 'package pages

// UT_DEMO is read once in config.Init; os.Getenv("UT_DEMO") must not appear here.
func zzNothing() {}'
expect_pass "a comment-only mention"
clear_fixtures

plant "zz_guard_demo_env_child_test.go" 'package pages

import "testing"

func TestZzDemo(t *testing.T) { t.Setenv("UT_DEMO", "1") }'
expect_pass "a _test.go file setting UT_DEMO"
clear_fixtures

plant "zz_guard_demo_env_childenv.go" 'package pages

func zzChildEnv() []string { return []string{"UT_DEMO=1", "UT_DEMO_TOKEN=" + "x"} }'
expect_pass "a child-process env assignment (\"UT_DEMO=1\")"
clear_fixtures

plant "zz_guard_demo_env_environ.go" 'package pages

import (
	"os"
	"strings"
)

func zzDemo() bool {
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "UT_DEMO=") {
			return true
		}
	}
	return false
}'
expect_fail "an os.Environ scan matching the \"UT_DEMO=\" prefix"
clear_fixtures

plant "zz_guard_demo_env_environ_token.go" 'package pages

import (
	"os"
	"strings"
)

func zzDemoToken() string {
	for _, e := range os.Environ() {
		if v, ok := strings.CutPrefix(e, "UT_DEMO_TOKEN="); ok {
			return v
		}
	}
	return ""
}'
expect_fail "an os.Environ scan matching the \"UT_DEMO_TOKEN=\" prefix"
clear_fixtures

plant "zz_guard_demo_env_environ_allow.go" 'package pages

import "os"

func zzChildEnv() []string {
	return append(os.Environ(), "UT_DEMO=1") // demo-env-guard:allow zz child env built from os.Environ
}'
expect_pass "an os.Environ file whose \"UT_DEMO=\" line carries the allow marker"
clear_fixtures

plant "zz_guard_demo_env_allow.go" 'package pages

const zzDemoKey = "UT_DEMO" // demo-env-guard:allow zz guard test fixture'
expect_pass "a line carrying an explicit demo-env-guard:allow marker"
clear_fixtures

# Baseline: the real, unmodified codebase passes.
expect_pass "the clean codebase"

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-demo-env_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-demo-env_test.sh: all cases passed"
exit 0
