---
id: backups
title: Backups
section: Running the business
order: 250
public: true
summary: Snapshots of all your shop data (catalog, sales, settings) that you can download and keep somewhere safe.
keywords: [backup, restore, restore backup, download, copy, uninstall, remove, replace, new till, broken, gdpr, cloud, off-site, encrypted]
routes: [/backups/cloud-restore, /backups/cloud-key]
---

# Backups

![Backups card](img/backups-card.png)

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

After a restore, receipt numbers and Z numbers jump ahead, so a number the till issued after the backup was made is never used twice, also when you restore the same or an older backup again. The first day-close report afterwards says that the till was restored from a backup and which numbers it continues from.

A backup replaces everything, which is different from undoing Clear transaction history: to bring back a cleared batch of sales without touching the rest, use Reset archives (see [Data management](/help/data-management)). Also, a backup made before you erased a customer still holds that customer until it is deleted, and restoring it brings them back (see [Erase a customer](/help/data-management)).

## Restoring from a file

Use this on a new till, or on one that replaces a broken till, with a backup file you copied off the old till (a USB stick, another computer, the Downloads folder).

1. Settings → Backups → **Restore from a file**: choose the file (it is named `unitill-pos-….db`) and press **Check the file**.
2. The till checks that the file is a Universal Till backup of this shop, made by this version or an older one, and shows the shop name, how many sales it holds and the newest sale. Sales made after that are not in the backup.
3. Press **Restore this backup** and confirm with your manager PIN. The till backs up its current data first; then restart it as described above.

Use a file from the old till's `backups` folder or one saved with **Save to Downloads**. If you copy the live database file `unitill-pos.db` from a broken till's disk instead, copy the `unitill-pos.db-wal` and `unitill-pos.db-shm` files next to it too, or the last sales are missing.

At the end of setup, the welcome page has a **Restore a backup** button that opens the same place.

A restored backup brings back products, sales, receipts, staff with their PINs, customers, settings, stock levels and photos as they were when the backup was made. Card terminal and payment plugin keys are not in a backup, and neither is the till's connection to the cloud. If you restore a phone or iCloud backup onto another device, a phone-to-phone transfer brings the till's data along, or you copy the database onto another device, that device starts unregistered and shows **Restored on a new device — connect again**: pair it with the shop again or register it as a new store in Settings → Till registration. A main till restored this way also moves its receipt numbers and Z numbers ahead, just like a restore from Settings → Backups, and its first day-close report says so. Enter plugin keys and passwords again on each plugin's settings page; a field that says it can't be read needs a new value. A joined till instead shows **Join the main till again** — see Multiple tills.

If you copy the whole till folder, or its SD card, onto other hardware, the copy moves its receipt numbers and Z numbers ahead the first time it starts there; the original keeps counting as before. A copy of the whole disk onto another PC has the same machine id and is not noticed. A joined till copied this way shows **Join the main till again**, and so does one that only looked moved (for example after reinstalling the operating system): join the main till again. Reinstalling or moving to a new Mac can look like a copy and also moves the numbers ahead — a gap, never a repeated number. The official Docker image has no machine id, so it is not checked. To set your own id, put `UT_HOST_ID` in the environment, with fixed text that is different on every machine, and set it on the original till before you copy. A `UT_HOST_ID` copied along with the folder hides the copy. Use `off` to turn the check off.

## Cloud backups

On a plan that includes cloud backup, the main till copies its newest daily backup to the cloud every day, in the background, so a lost or broken till does not take your shop's history with it. The copy is encrypted on the till before it leaves it, with a key your till makes for your shop and keeps on the till (outside the database, so it is never inside a backup). Keeping a protected copy of that key in our cloud is **your choice**, and you can change it at any time:

- **Yes, keep a copy in the cloud:** you can restore after losing this till by signing in on my.universaltill.com. We hold a wrapped copy of the key, so we could technically decrypt your backups, and anyone who gets into your account could approve a restore.
- **No, keep it only on this till:** only this till and the copy you save hold the key. If you lose this till and your saved key, your backups cannot be read by anyone, including us.

Until you choose, the card (and a **Backup key choice** chip in the status bar) asks, and copies to the cloud run anyway. Either way a restore needs a fresh sign-in with your second factor on my.universaltill.com, is logged, and you get an email every time. Staff have no way to approve it for you. Choosing No on the till only stops the till keeping a copy; a copy already in the cloud is removed by the owner on my.universaltill.com, with a fresh sign-in. When the owner removes it there, the till shows that it was turned off by the owner.

- Settings → Backups → **Cloud backups** shows when the last copy was made. Selling is never slowed down or blocked by a copy.
- **Copy to the cloud now** starts one straight away (it asks for the manager PIN, like Back up now).
- The cloud keeps a backup for each of the last 30 days and one for the first of each of the last 12 months. Only the main till makes cloud backups; additional tills do not.
- If the copies keep failing for 2 days, managers see **Cloud backup failing** in the status bar. The card says why (for example not enough free space on the till, or no internet).
- **Save your key:** Settings → Backups → Cloud backups → **Save your key** (managers and admins, with the manager PIN) shows the key as a code that starts with `UTK1-`. Print it, send it to the receipt printer, or download the key file, and keep it somewhere safe. Anyone who has this key and your backups can read them. With the cloud copy switched off, this saved key is the only way back if the till is lost.
- If the cloud has no copy of your key and you restore on a new till, the restore asks you to **enter the backup key you saved** (type it, or open the key file). The till checks that it is complete and belongs to your shop before anything is opened.
- If Settings → Backups says **your shop already has a backup key in the cloud, but this till does not have it yet** (for example a new main till that did not restore), press **Link this till to your backup key** (managers and admins, with the manager PIN). The till shows a code and a QR code; approve it on my.universaltill.com as the owner. The till then keeps the key and copies resume. If there is no copy in the cloud, use **Enter your saved key** instead. If you have lost the key too, **Start a new backup key** makes a new one: new backups work again, but backups made before today can only be opened with the key you saved.
- Changing your password or second factor changes nothing about the key. Rotating the key is not available yet. If you lose access to your account, regain it through account recovery first; the key and your backups wait for you. Until then a working till keeps backing up, but a destroyed till cannot be restored from the cloud.
- If the plan ends, the backups are kept for 30 days and then deleted. Reconnecting a plan within that time keeps them.

## Restore a cloud backup on a new till

Use this when a till was lost or broken and a new till has taken its place as the shop's main till.

- **Step 1.** On the new till: Settings → Backups → **Restore from the cloud** (managers and admins only). On a new till it is also offered on the home screen, and on the last setup step after a replace.
- **Step 2.** Choose a backup. The list says when each was made: sales after that moment are not in it. If this till already has sales, the page says they will be replaced.
- **Step 3.** Press **Restore this backup** and confirm with your manager PIN.
- **Step 4.** The till shows an 8-character code and a QR code. On your phone, open my.universaltill.com, sign in again as the shop owner, and approve the restore with that code. The approval runs out after 15 minutes. Only approve it when you are at this till.
- **Step 5.** The till downloads and checks the backup, backs up its current data first, then shows **Restart now**. Nothing changes until it restarts.

The new till keeps its own connection to your shop's cloud account and its own receipt prefix. If something goes wrong (the owner declines, the code expires, the file fails its check, it is another shop's), the till says so and your data is unchanged.

## Replacing a till

A backup is the only way to bring sales, receipts, customers and staff PINs back on a new till: the cloud does not keep them on any plan. Keep a recent copy off the till. Managers see **No backup copied off this till in 7 days** in the status bar until a backup has been saved off it; the reminder disappears as soon as a copy is saved.

Where the till keeps its backups (the last 7 days by default):

- macOS: `~/Library/Application Support/UniversalTill/backups`
- Windows: `%LOCALAPPDATA%\UniversalTill\backups`
- Linux: `~/.local/share/universal-till/backups` by default (installed from the `.deb` package: `/opt/unitill/data/backups`)
- Android and iOS: inside the app, out of reach — use **Save to Downloads**. On Android this opens the device's save screen: choose Downloads, or a USB drive if that is where your storage is.

1. If the old till still starts: Settings → Backups → **Back up now**, then **Save to Downloads** next to the new backup, and copy the file to a USB stick or another computer.
2. If it does not start but its disk can be read, take the newest file from its `backups` folder.
3. On the new till, finish setup (for a shop on my.universaltill.com, join it as the main till), then press **Restore a backup** on the welcome page, or open Settings → Backups → **Restore from a file**, and follow "Restoring from a file" above.
4. Restore before the new till rings its first sale: a restore replaces the till's data, it does not merge it.

Sales made on the old till after the backup was taken are not in it.

## Deleting a backup and choosing how long backups are kept

In Settings → Backups, a manager or admin sees **Delete** on every backup except the newest one. The till asks you to confirm, then removes that file for good. The newest backup has no Delete button and is never deleted, by hand or automatically. A cashier cannot delete backups.

Under the list, **Keep backups for (days)** sets how many days of backups this till keeps (1 to 365, 7 by default). Each till has its own number, because each till has its own disk. A smaller number takes effect at the next backup, or the next daily clean-up; saving it never deletes anything by itself. Restoring a backup also brings back the number that was saved in it.

## Automatic clean-up

Once a day the till removes files it no longer needs, so its disk doesn't fill up:

- Backups: the last 7 days are kept (you can change the number of days, see below). Older ones are deleted whenever a new backup is made and again once a day. The newest backup is always kept. Together they never take more than a quarter of the free disk space: the oldest go first, but the newest 3 are always kept.
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
