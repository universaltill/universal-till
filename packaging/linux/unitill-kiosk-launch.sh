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
exec "$BROWSER" \
  --kiosk --noerrdialogs --disable-infobars --no-first-run \
  --disable-session-crashed-bubble --disable-features=TranslateUI \
  --check-for-update-interval=31536000 \
  --password-store=basic \
  --ozone-platform=wayland \
  "$URL"
