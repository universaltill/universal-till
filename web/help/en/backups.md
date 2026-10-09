---
id: backups
title: Backups
section: Running the business
order: 250
summary: Snapshots of all your shop data (catalog, sales, settings) that you can download and keep somewhere safe.
keywords: [backup, restore, download, copy, uninstall, remove]
---

# Backups

Snapshots of all your shop data (catalog, sales, settings) that you can download and keep somewhere safe. Each backup also holds the photos you uploaded for items and categories, and your receipt logo, so the one file you download brings them back too.

## How to use it

1. Settings → Backups: create a backup any time. If it says the photos could not be added, the backup holds your data but not your photos — try again before you wipe or replace the till.
2. Download saves a copy to your Downloads folder — keep one off the till.

## Restoring a backup

Restoring replaces all current data with the chosen backup — confirm
with your manager PIN, since this can't be undone from the settings page
itself (the replaced data is kept as its own backup, in case you need it
back). After restoring, click **Restart now** and the till restarts
itself — no need to reach for a keyboard or a plug. On Windows the till
can't restart itself yet — close the window and reopen Universal Till
instead.

## Automatic clean-up

Once a day the till removes files it no longer needs, so its disk doesn't fill up:

- Backups: the newest 14 are kept. Together they never take more than a quarter of the free disk space: the oldest go first, but the newest 3 are always kept.
- The copy of your data set aside when you restore a backup: kept for as long as the law requires you to keep sales records; after that, only the newest 3 are kept, and none older than 30 days.
- Issue reports that couldn't be sent: removed after 7 days.
- Downloaded updates: removed after 7 days.
- Uploaded files left behind when the till stopped in the middle of an upload or import: removed after 24 hours.

If the disk gets almost full — less than 500 MB free, or less than a tenth of the disk, whichever is larger — the till cleans up at once instead of waiting for the next day, and keeps only the minimum: the newest 3 backups, the newest copy set aside by a restore, and issue reports and downloaded updates from the last day. Managers then see **Storage almost full** in the status bar until there is room again. Selling carries on; to free more space, remove files you don't need from the device, or ask whoever set it up.

The clean-up never removes sales, receipts, the audit log, Z reports or any other record you must keep by law. Each till cleans only its own disk.

## Removing the till from a Linux box

If the till was installed from the `.deb` package, run `sudo
unitill-uninstall` in a terminal to remove it. It creates a verified
backup of your shop data first (saved to your home folder) and then asks
whether to keep the data — keeping it means a later reinstall carries on
where you left off. Deleting the data needs you to type `DELETE`, so it
can't happen by accident.
