---
id: backups
title: Backups
section: Running the business
order: 250
summary: Snapshots of all your shop data (catalog, sales, settings) that you can download and keep somewhere safe.
keywords: [backup, restore, download, copy, uninstall, remove]
---

# Backups

Snapshots of all your shop data (catalog, sales, settings) that you can download and keep somewhere safe.

## How to use it

1. Settings → Backups: create a backup any time.
2. Download saves a copy to your Downloads folder — keep one off the till.

## Removing the till from a Linux box

If the till was installed from the `.deb` package, run `sudo
unitill-uninstall` in a terminal to remove it. It creates a verified
backup of your shop data first (saved to your home folder) and then asks
whether to keep the data — keeping it means a later reinstall carries on
where you left off. Deleting the data needs you to type `DELETE`, so it
can't happen by accident.
