---
id: updates
title: Software updates
section: Connecting & extending
order: 350
summary: The till checks for new versions and tells you when one is available; on most platforms it updates itself with one click.
keywords: [update, version, upgrade, schedule]
---

# Software updates

The till checks for new versions and tells you when one is available; on most platforms it updates itself with one click.

## How to use it

1. Settings → Software update → Check now shows whether you are up to date.
2. When an update is offered, click Update now — the app restarts on the new version. On a Linux computer, the desktop app also restarts itself within about a minute when a system upgrade (apt) installs a new version, and if the till's page stops showing in its window, the app reloads it by itself until the till answers again.
3. The status bar's update chip mirrors this. Where the till can't install an update itself — a portable Windows zip, or an Intel Mac — it links to the download page instead. On a kiosk without in-app update it's just plain text with nothing to tap. On a Linux till that updates automatically but can't write to its own install folder, the chip says the till can't install the update by itself and links to this page: reinstall the latest .deb package — that makes the folder writable again (for any other kind of install, give the till's user write access to its folder) — and the next night's update runs as usual.

## Automatic updates

The till updates itself overnight unless you switch it off.

1. Settings → Software update → Update automatically is on from the start, at 03:00. Each till picks its own moment in the 30 minutes after that time, so several tills don't all restart at once.
2. It never restarts in the middle of a sale. If a basket (including a self-order or table order) still has items, it waits until the basket is empty. If that doesn't happen within half an hour, or the till was switched off at the time, it tries again the next night.
3. To stop automatic updates, untick Update automatically and click Save. The till keeps that choice. On the main till, this also stops its additional tills from following it; an additional till always takes this choice from its main till, so its Settings → Software update shows the main till's version it follows instead of the box — change automatic updates on the main till.
4. An additional till follows the main till: when the main till runs a newer version, the additional till installs exactly that version at the next moment with no open sale. Android additional tills, and Windows ones installed from the portable zip, show a 'needed' note instead, until they can install by themselves.
5. On Windows the till downloads the signed installer and checks it. Then the till closes, installs the update and opens again by itself, which takes about a minute. Android tills can't install updates by themselves yet. Update them by hand as described above. If you run the Windows installer yourself, it closes Universal Till first if it is still open, so finish any sale in progress before you start it.

## What's new in each version

After an update, the till tells you what changed — in plain words, in the till's language.

1. Settings → About shows the version this till runs, when it first started on this till, and "What's new": the notes for this version and the few before it, newest first, grouped into New, Improved and Fixed.
2. The first time a manager or admin opens the till after an update, a small "Updated to … — see what's new" note appears in the status bar. Tap it to read the notes, or tap × to hide it. It never gets in the way of selling and it shows only once.
3. Cashiers never see this note, and neither do customers at a self-order kiosk. A newly installed till doesn't show it either.
4. The notes are built into the till, so they work without an internet connection. If a note isn't translated into your language yet, it's shown in English.
5. Before you install an update, Settings → Software update shows a manager or admin what the new version brings: the notes for every version the update includes, newest first, in the till's language where translated and in English otherwise. These notes are downloaded only when you open that page. If they can't be loaded, the till says "Release notes unavailable" — on Windows and Mac with a link to the release page — and you can still install the update as usual.

## On an Android till

The Android app cannot replace itself the way the desktop versions do, so it hands the new version to Android's own installer instead. The steps are slightly different:

1. Tap the green update chip at the bottom of the screen, then tap it again to confirm. That is the whole thing — signed in as a manager you are not asked for a PIN, exactly as on Windows and Mac.
2. Android downloads the new app — around 140 MB, so give it a moment on a slow connection; the chip says "Downloading" the whole time — and then shows its own "Do you want to install this update?" screen. Confirm there.
3. The first time you do this, Android may ask you to allow Universal Till to install apps. Allow it, then tap the chip again.
4. Settings → Software update does the same thing, and also shows which version you are running and which is available.
5. The manager PIN box and the Download button only appear when a new version is actually available — on an up-to-date till there is nothing there to tap. If the till re-checks as you tap and finds you are already on the newest build, it tells you so instead of downloading anything.

You are asked for a manager PIN in two cases: you are signed in as a cashier, or the till is in self-order mode. In self-order mode installing would also unlock the kiosk, so the PIN is required there even for a manager.

During first-time setup the till will tell you an update exists but will not offer to install it — finish setting the till up, then update from Settings.
