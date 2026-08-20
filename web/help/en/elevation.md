---
id: elevation
title: Manager approval on the spot
section: Connecting & extending
order: 341
summary: When a screen tells you manager approval is required, a manager or admin can approve it right there with their PIN — no need to sign out and back in.
keywords: [pin, approval, manager pin, override, elevation, permission]
---

# Manager approval on the spot

Some actions — changing a permission, promoting a till, running a backup,
running or reprinting the end-of-day report, and others — need a manager's
or admin's say-so. If your own account isn't
allowed to do one of these, the screen doesn't just refuse: it opens a small
PIN prompt right there.

## How to use it

1. Try the action as normal. If your role isn't allowed to do it, a dialog
   appears asking for a manager's or admin's PIN instead of a plain error.
2. Have a manager or admin type their own PIN into that dialog and confirm.
3. If the PIN is correct and that person is actually allowed to do the
   action, it goes ahead right away — as them, not as you. Nobody signs out
   or switches accounts.
4. A wrong PIN, too many attempts, or a PIN that belongs to someone who
   still isn't allowed to do that particular action all show a clear reason
   in the same dialog, so you know what to try next.

## What gets recorded

The action is journaled as done by the approving manager or admin — the same
as everything else in the audit trail (Reports → Audit). The account that
was originally blocked is also kept on that same record, so the full story
of who tried it and who actually approved it is never lost, even though
today's Audit screen shows the approver's name as the entry's actor.
