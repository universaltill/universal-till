# shellcheck shell=bash
#
# Sourced by ios-sim-smoke.sh (ut-docs#5201). The launch screenshot used to
# be taken a fixed 8 s after the till answered /healthz; a simulator that
# spends minutes on its first-boot data migration hadn't drawn the WebView
# by then and the blank check failed a healthy app. This polls instead.
#
# wait_for_drawn <shoot> <check> <png> <deadline-s> [interval-s]
#   Runs `<shoot> <png>` then `<check> <png>` until the check passes or the
#   deadline runs out (one last attempt is always made at the deadline). A
#   failed screenshot counts as a failed attempt. Only the final attempt's
#   output is printed, so the earlier, expected "looks blank" errors don't
#   become workflow annotations and the failure message stays the check's
#   own. Returns the check's verdict.
wait_for_drawn() {
  local shoot="$1" check="$2" png="$3"
  local deadline=$((SECONDS + $4)) interval="${5:-3}"
  local log attempt=0 rc
  # Outside the screenshot folder: that folder is uploaded as an artifact.
  log="$(mktemp)"
  while :; do
    attempt=$((attempt + 1))
    rc=0
    if ! "$shoot" "$png" >"$log" 2>&1; then
      echo "::error::taking the screenshot $png failed" >>"$log"
      rc=1
    elif ! "$check" "$png" >"$log" 2>&1; then
      rc=1
    fi
    if [ "$rc" -eq 0 ] || [ "$SECONDS" -ge "$deadline" ]; then
      cat "$log"
      rm -f "$log"
      if [ "$rc" -eq 0 ]; then
        echo "drawn after ${attempt} screenshot(s)"
      else
        echo "still blank after ${attempt} screenshot(s)"
      fi
      return "$rc"
    fi
    sleep "$interval"
  done
}
