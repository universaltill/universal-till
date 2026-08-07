#!/usr/bin/env bash
# Runs INSIDE the cage kiosk compositor: wait for the till service, then show
# it fullscreen in Chromium kiosk mode. Installed by unitill-kiosk-setup.sh.
URL="${UT_KIOSK_URL:-http://127.0.0.1:8080}"

# The till service starts in parallel on boot; wait (up to ~2 min) for it.
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$URL/healthz" && break
  sleep 2
done

BROWSER="$(command -v chromium-browser || command -v chromium)"
# --password-store=basic: without it, Chromium's first touch of its password
# manager pops a blocking "keyring is locked" GTK dialog over the kiosk with
# no way to dismiss it from the touchscreen (confirmed live, 2026-07-29).

# The portal is D-Bus-activated, so it does NOT inherit this script's
# environment — it has to be pushed into the activation environment explicitly,
# or xdg-desktop-portal cannot tell which backend to use and screen capture
# silently offers nothing to select (ut-docs#395, confirmed on field hardware).
# "wlroots" is what wlr.portal's UseIn= declares, and cage is wlroots-based.
# Errors intentionally go to the unit's own journal (StandardError=journal on
# unitill-kiosk.service), not /dev/null: this is the one path in the chain
# that fails silently by design (a missing dbus-bin, no session bus yet), and
# ut-docs#395 was exactly a silent portal failure with zero operator signal —
# `|| true` alone (without discarding stderr) still lets the kiosk boot while
# leaving a trace in `journalctl -u unitill-kiosk` for whoever investigates a
# "capture still doesn't work" follow-up report.
export XDG_CURRENT_DESKTOP=wlroots
dbus-update-activation-environment --systemd \
  XDG_CURRENT_DESKTOP WAYLAND_DISPLAY XDG_SESSION_TYPE || true

exec "$BROWSER" \
  --kiosk --noerrdialogs --disable-infobars --no-first-run \
  --disable-session-crashed-bubble --disable-features=TranslateUI \
  --check-for-update-interval=31536000 \
  --password-store=basic \
  --ozone-platform=wayland \
  "$URL"
