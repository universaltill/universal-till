---
id: backups
title: Backups
section: Running the business
order: 250
summary: Snapshots of all your shop data (catalog, sales, settings) that you can download and keep somewhere safe.
keywords: [backup, restore, download, copy, uninstall, remove, replace, new till, broken]
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

A restore keeps this till's own connection to your shop's cloud account, also when the backup was made on another till. If the file turns out not to be a backup this till can use, it keeps your current data, and Settings → Backups says the restore was not applied.

## Restoring from a file

Use this on a new till, or on one that replaces a broken till, with a backup file you copied off the old till (a USB stick, another computer, the Downloads folder).

1. Settings → Backups → **Restore from a file**: choose the file (it is named `unitill-pos-….db`) and press **Check the file**.
2. The till checks that the file is a Universal Till backup of this shop, made by this version or an older one, and shows the shop name, how many sales it holds and the newest sale. Sales made after that are not in the backup.
3. Press **Restore this backup** and confirm with your manager PIN. The till backs up its current data first; then restart it as described above.

Use a file from the old till's `backups` folder or one saved with **Save to Downloads**. If you copy the live database file `unitill-pos.db` from a broken till's disk instead, copy the `unitill-pos.db-wal` and `unitill-pos.db-shm` files next to it too, or the last sales are missing.

At the end of setup, the welcome page has a **Restore a backup** button that opens the same place.

A restored backup brings back products, sales, receipts, staff with their PINs, customers, settings, stock levels and photos as they were when the backup was made. Card terminal and payment plugin keys are not in a backup, and neither is the till's connection to the cloud. If you restore a phone or iCloud backup onto another device, or copy the database onto another device, that device starts unregistered and shows **Restored on a new device — connect again**: pair it with the shop again or register it as a new store in Settings → Till registration. Enter plugin keys and passwords again on each plugin's settings page; a field that says it can't be read needs a new value.

## Replacing a till

A backup is the only way to bring sales, receipts, customers and staff PINs back on a new till: the cloud does not keep them on any plan. Keep a recent copy off the till. Managers see **No backup copied off this till in 7 days** in the status bar until a backup has been saved off it.

Where the till keeps its backups (the newest 14):

- macOS: `~/Library/Application Support/UniversalTill/backups`
- Windows: `%LOCALAPPDATA%\UniversalTill\backups`
- Linux: `~/.local/share/universal-till/backups` by default (installed from the `.deb` package: `/opt/unitill/data/backups`)
- Android and iOS: inside the app, out of reach — use **Save to Downloads**.

1. If the old till still starts: Settings → Backups → **Back up now**, then **Save to Downloads** next to the new backup, and copy the file to a USB stick or another computer.
2. If it does not start but its disk can be read, take the newest file from its `backups` folder.
3. On the new till, finish setup (for a shop on my.universaltill.com, join it as the main till), then press **Restore a backup** on the welcome page, or open Settings → Backups → **Restore from a file**, and follow "Restoring from a file" above.
4. Restore before the new till rings its first sale: a restore replaces the till's data, it does not merge it.

Sales made on the old till after the backup was taken are not in it.

## Automatic clean-up

Once a day the till removes files it no longer needs, so its disk doesn't fill up:

- Backups: the newest 14 are kept. Together they never take more than a quarter of the free disk space: the oldest go first, but the newest 3 are always kept.
- The copy of your data set aside when you restore a backup: kept for as long as the law requires you to keep sales records; after that, only the newest 3 are kept, and none older than 30 days.
- Issue reports that couldn't be sent: removed after 7 days.
- Downloaded updates: removed after 7 days.
- Uploaded files left behind when the till stopped in the middle of an upload or import: removed after 24 hours.

If the disk gets almost full — less than 500 MB free, or less than a tenth of the disk (at most 5 GB), whichever is larger — the till cleans up at once instead of waiting for the next day, and keeps only the minimum: the newest 3 backups, the newest copy set aside by a restore, and issue reports and downloaded updates from the last day. Managers then see **Storage almost full** in the status bar until there is room again. Selling carries on; to free more space, remove files you don't need from the device, or ask whoever set it up.

The clean-up never removes sales, receipts, the audit log, Z reports or any other record you must keep by law. Each till cleans only its own disk.

## Removing the till from a Linux box

If the till was installed from the `.deb` package, run `sudo
unitill-uninstall` in a terminal to remove it. It creates a verified
backup of your shop data first (saved to your home folder) and then asks
whether to keep the data — keeping it means a later reinstall carries on
where you left off. Deleting the data needs you to type `DELETE`, so it
can't happen by accident.
