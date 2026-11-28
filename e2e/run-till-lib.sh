#!/usr/bin/env bash
# Sourced by the e2e/run-till*.sh launchers (ut-docs#7177).
#
# Every launcher makes a throwaway data dir (DB, plugins, TLS and a ~100 MB
# copy of the till binary) and removes it from an EXIT trap. `exec "$BIN"`
# replaces the shell, so that trap never ran and every run left the dir
# behind. ut_e2e_serve runs the binary as a child instead and turns
# SIGTERM/SIGINT (what Playwright's gracefulShutdown sends the process group,
# see till-webserver.ts) into "stop the child, wait for it, exit" — so the
# caller's EXIT trap runs only once nothing can write into the data dir.
#
# Usage: ut_e2e_serve "$BIN" [args...]   (after the caller's own `trap ... EXIT`)
UT_E2E_TILL_PID=""

ut_e2e_stop_till() {
  if [[ -n "${UT_E2E_TILL_PID}" ]]; then
    kill "${UT_E2E_TILL_PID}" 2>/dev/null || true
    wait "${UT_E2E_TILL_PID}" 2>/dev/null || true
  fi
  exit 143
}

ut_e2e_serve() {
  # Trap first: a signal between starting the child and installing the trap
  # would otherwise run the caller's EXIT trap while the till still runs.
  trap ut_e2e_stop_till INT TERM
  "$@" &
  UT_E2E_TILL_PID=$!
  local rc=0
  wait "${UT_E2E_TILL_PID}" || rc=$?
  return "${rc}"
}
