#!/bin/bash
# Universal Till POS — uninstaller (macOS). Double-click in Finder to remove
# the till. It stops a running till, then asks before deleting your shop data
# (database, plugins, backups). Deleting the app folder itself is the last step
# it points you to — this script can't delete the folder it's running from.
cd "$(dirname "$0")" || exit 1

DATA_DIR="$HOME/Library/Application Support/UniversalTill"

echo "Universal Till — uninstall"
echo

# 1) Stop a running till so nothing is holding the database open.
# Wait for it to really exit (ut-docs#5049): deleting the data folder under a
# live server leaves it running against a deleted database. Delete nothing
# until it is gone.
if pgrep -u "$(id -u)" -x unitill-pos >/dev/null 2>&1; then
  echo "Stopping the running till…"
fi
osascript -e 'quit app "Universal Till"' 2>/dev/null || true
pkill -u "$(id -u)" -x unitill-pos 2>/dev/null || true
n=0
while pgrep -u "$(id -u)" -x unitill-pos >/dev/null 2>&1 && [ "$n" -lt 15 ]; do
  sleep 1
  n=$((n + 1))
done
if pgrep -u "$(id -u)" -x unitill-pos >/dev/null 2>&1; then
  pkill -9 -u "$(id -u)" -x unitill-pos 2>/dev/null || true
  n=0
  while pgrep -u "$(id -u)" -x unitill-pos >/dev/null 2>&1 && [ "$n" -lt 5 ]; do
    sleep 1
    n=$((n + 1))
  done
fi
if pgrep -u "$(id -u)" -x unitill-pos >/dev/null 2>&1; then
  echo "The till is still running and could not be stopped."
  echo "Quit Universal Till, then run this uninstaller again."
  echo "Nothing was deleted."
  read -r -p "Press Return to close." _
  exit 1
fi

# 2) Shop data is precious (sales history, receipts) — never delete it silently.
if [ -d "$DATA_DIR" ]; then
  echo "Your shop data is stored at:"
  echo "  $DATA_DIR"
  echo "This holds your database, installed plugins, and backups."
  echo
  printf "Delete ALL shop data too? This cannot be undone. [y/N] "
  read -r reply
  case "$reply" in
    [yY]|[yY][eE][sS])
      rm -rf "$DATA_DIR"
      echo "Shop data deleted."
      ;;
    *)
      echo "Kept your shop data at $DATA_DIR"
      echo "(A future install will pick it up again.)"
      ;;
  esac
else
  echo "No shop data found at $DATA_DIR — nothing to delete."
fi

echo
echo "Almost done. To finish removing the app, drag this folder to the Trash:"
echo "  $(pwd)"
echo
echo "Done."
read -r -p "Press Return to close." _
