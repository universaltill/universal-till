---
id: recovery
title: Recovery screen
section: Running the business
order: 255
summary: What you see if the till fails to start, and what each action on that screen does.
keywords: [recovery, error, won't start, crash, retry, safe mode, corrupt, log, log file, troubleshooting]
---

# Recovery screen

If the till fails to start (most often a software update that didn't
finish cleanly, or the storage device running out of space), it shows a
recovery screen instead of a blank or frozen screen — this screen can't
carry its own "?" link back to this manual, since the till itself isn't
running normally yet, so find this topic from the manual's own search or
topic list instead.

Your data is never touched by a failed start on its own — today the
recovery screen only ever reads (Retry, and the read-only safe-mode view
below); it never writes anything.

## What the screen shows

- A plain-language explanation of what went wrong, and a **reference
  code** — read this out if you call support.
- **Retry** — tries to start again. This is usually all that's needed if
  the problem was temporary (e.g. it happened mid-update).
- When the failure happened during a database update specifically, a
  **safe mode** section appears with a link to see (and export) **today's
  sales**, read-only, while the underlying problem is being fixed — so you
  can still tell a customer their total or print today's figures even
  while the till itself won't fully start.

## If Retry doesn't work

Contact support with the reference code shown on screen. Depending on the
cause, the fix may be a further software update, or restoring the till's
last automatic backup (see the Backups topic) — support will tell you
which.

## Where the till's log is

The till writes a technical log that support may ask for:

- **Windows:** `%LOCALAPPDATA%\UniversalTill\logs` — paste that into the File Explorer address bar.
- **Mac:** `~/Library/Application Support/UniversalTill/logs`
- **Linux desktop:** `~/.local/share/universal-till/logs`
- **Linux till installed as a service:** `/opt/unitill/data/logs`

`till.log` is the newest file, `till.log.1` to `till.log.4` are older ones, and `desktop.log` is the app window's own log. Passwords and tokens are removed before anything is written, and the folder never grows past about 25 MB. While the till is running, a manager can also find the folder under Settings → Diagnostic mode → **Log files**, with buttons to copy its path and a diagnostics summary to send to support. The Android app does not write these files.

## On an Android till: a single page fails to load

This is different from the recovery screen above, which is about the
**whole till** failing to start. Here the till is running fine, but one
page briefly couldn't load — the network blipped mid-navigation, or the
till's own service was still coming up. A grey bar reading "This page
couldn't load" appears at the bottom of the screen with a **Back to
till** button; tap it to return to the sale screen. The screen above the
bar may briefly show a technical message in English regardless of your
till's language — that's normal, just use the button below it. If it
keeps happening, contact support with which screen you were trying to
open.
