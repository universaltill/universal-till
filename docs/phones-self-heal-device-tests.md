# Phones: till self-heal device test matrix (ut-docs#7097, ADR-0222, slice T8)

For the owner to run on the **iPhone** (the main target) and an **Android
phone**. Everything here needs a real device: the Go engine is tested in
`internal/credheal` and `mobile/`, the foreground hook in `ForegroundThrottle`
tests; none of that proves the OS suspends and resumes the app the way a person
does. Write what you expected and what you saw for every row, with a
screenshot. A screen that merely shows something is not evidence.

## Before you start

- Cloud has `TILL_SELF_HEAL_ENABLED` on for your test shop (ADR-0222 rollout
  step 5). With the flag off every heal row below must show the OLD behaviour
  (row 0).
- A phone build that contains this change AND the till engine (PR #2760).
- The phone is a registered till of your shop with the shop claimed on
  my.universaltill.com. Note its till name. Open Settings > Cloud: it should say
  connected.
- How to "kill the token on the cloud side" (the dead-credential trigger):
  on my.universaltill.com make a rotation of this till's credential WITHOUT
  removing the till (support or a staging script; Do NOT use "Remove till": that
  is final and is row 10). If you have no such tool, ask the pipeline owner for
  the staging rotate call. Never edit the phone's database.

## Matrix

| # | Phone | State before | Do | Expect (pass) | Fail looks like |
|---|---|---|---|---|---|
| 0 | iPhone | Flag OFF in the cloud | Kill the token, wait one sync tick (about a minute) | Chip says the till needs pairing (today's wording). No "Reconnecting" for longer than one round. | Chip stuck on "Reconnecting" with the flag off |
| 1 | iPhone | Foreground, till screen open, selling works | Kill the token. Do nothing. | Within about one minute the status bar shows "Reconnecting this till to your shop. Selling works as normal.", then it goes away by itself. my. shows the same register (same name, same role) with a "Reconnected by itself" entry. | Chip stays, a pairing code is asked for, a second register appears |
| 2 | iPhone | Foreground, a basket with 2 items open | Do row 1 while the basket is open | The basket is untouched, you can finish the sale during "Reconnecting". The screen is never covered. | Basket lost, a dialog covers the till |
| 3 | iPhone | Till open | Press Home, wait 10 minutes (so iOS suspends the app), kill the token, wait 2 more minutes, open the app | Within about 30 s of opening the app the chip shows "Reconnecting..." and then clears. (This is the resume hook: a suspended app ran no timers.) | Nothing for several minutes after opening; or only after pulling to refresh |
| 4 | iPhone | Same as 3 but background for 30 minutes | Same | Same as 3. The till may have been restarted by the shell (check: sale basket gone is expected after a restart, and the till still heals) | Stuck, or a pairing code is asked |
| 5 | iPhone | Foreground | Pull down Notification Centre and close it 5 times in 10 s | No chip flicker, no slowdown. (The check is limited to once per 30 s.) | Repeated flashing |
| 6 | iPhone | Till is online | Open and close the app 5 times with the token NOT killed | Never any chip. my. shows no "reconnected" entry. A working credential is never rotated. | A "reconnected" entry although the token was fine |
| 7 | iPhone | Airplane mode ON, token killed | Open the app, sell something, turn Airplane mode off | Selling works offline. After the network returns the chip shows "Reconnecting..." and clears. Sale is in the cloud after sync. | Selling blocked; the sale lost |
| 8 | iPhone | Token killed, phone shows "Reconnecting" | Close the app (swipe away) and open it again within a minute | Heals after the restart (the engine saved its state). | Restarts with no chip and never heals; or a pairing code is asked |
| 9 | iPhone | Cloud shop switched off (`TILL_SELF_HEAL_STORES` without your shop) | Kill the token | Falls back to today's pairing message after one round; it must NOT say "Reconnecting" for more than a minute. | Endless "Reconnecting" |
| 10 | iPhone | Owner presses "Remove till" for this phone on my. | Open the app | Plain words "This till was removed from your shop's account. Its sales are saved on this till." with one button "Enter a code". No automatic reconnect, no new register. | The phone re-appears in the account by itself |
| 11 | iPhone | Owner "Replace this till" with another device | Open the app | "This till was replaced by ..." and one button "Join as an additional till". | Heals itself (it must not) |
| 12 | iPhone | **Restored** from an iCloud backup or a device-to-device transfer onto another iPhone | Open the app | The restored chip "Restored on a new device — connect again" with one button "Reconnect with a code". Nothing else. No new shop appears in the owner's account and no `register` call (check my.: the shop count did not go up). The key file is NOT there (it is excluded from backup), so no heal is attempted. Enter a code from my. (Make a code): the phone returns to the shop as a new device. | A new anonymous shop appears; or "Reconnecting" forever. (Row 13 of the ba-notes dead-end table; the register fix is slice T4, PR feat/7097-till-restored-roster: rerun this row once that is merged.) |
| 13 | iPhone | App deleted and reinstalled | Open the app, choose "I already set up my shop online", enter a code | New device in the shop; the old register shows "reconnects by itself" and stays until removed (one click). | Old register disappears by itself, or the phone cannot join |
| 14 | iPhone | Waiting for the Mac to approve a join request (phone shows the code) | Press Home for 2 minutes, come back | The phone is still waiting and shows the same code; the Mac still shows ONE pending request. **Known gap**: if the shell restarted the server meanwhile, the waiting state is lost (kept in memory, ut-docs follow-up). Record whether it happened; then press Join again and check the Mac shows ONE row (needs the roster slice T7). | Mac shows 2 or 3 rows from this phone |
| 15 | iPhone | Reader mode on (`UT_READER_MODE` build), paired reader | Remove this reader on the Mac | Phone shows "revoked" and clears the credential; pairing again works. Unrelated to the cloud credential: it must not show any "Reconnecting" chip. | Reader keeps trying forever |
| 16 | Android | Same as 1 on an Android phone (additional till of a Mac main) | Kill the token | Heals, role stays "additional" on my. | Becomes main, or a second register |
| 17 | Android | Same as 3 (Home 10 minutes) | Same | Heals. (A foreground service keeps the engine running, so it may already have healed before you opened the app: that is fine, note when.) | Needs a code |
| 18 | Android | Restored via Android Backup / device transfer | Open the app | As row 12. | As row 12 |
| 19 | both | 320 pt wide screen (iPhone SE size or Display Zoom) and the longest language (German) | Trigger each of rows 1, 10, 11, 12 | The chip text and its ONE button are fully readable, not clipped, button at least 44 pt high. | Clipped text, two buttons, button off-screen |

## What to send back

The row number, pass or fail, one screenshot of the chip and one of my.
"Tills" for each failing row, and for rows 3, 4 and 14 the time you opened the
app and the time the chip cleared. The phone's log (Settings > Support > share
log) for any fail; it never contains a token.
