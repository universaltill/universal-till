---
id: users
title: Users, PINs & shifts
section: Connecting & extending
order: 340
summary: Separate cashier and manager accounts with PIN login, and shift tracking so you know who sold what and when the drawer was counted.
routes: [/users, /pin, /login, /setup, /users/permissions]
keywords: [pin, staff, roles, sign in, lock, permissions]
---

# Users, PINs & shifts

Separate cashier and manager accounts with PIN login, and shift tracking so you know who sold what and when the drawer was counted.

## How to use it

1. Manage accounts under Users; managers can access settings and reports, cashiers can sell.
2. Each person signs in with their PIN on the till.
3. Open and close shifts to track drawer counts per person.
4. The first-boot setup wizard's shop name step also asks what to call this till (pre-filled with "Till 1" so you can accept it with one tap) — helpful once you have more than one till, and changeable later from Settings.
5. The wizard also asks what kind of shop this is (café, retail, service trade, hospitality, market stall or other) and whether to load sample data — a small starter catalogue, 3 sample customers, and 3 sample promo codes (including a 10%-off code) — so you can try the till out. Both are optional: it's the same generic set whichever shop type you pick, the catalogue items are marked with a SAMPLE badge, and you can remove all of it any time from Settings → Data.
6. The wizard's language and country steps start pre-filled from this device's own system language and timezone — no internet lookup, so it works before the till is even online. The language step stays a one-tap choice among buttons. The country step shows only the detected country at first, pre-selected; tap "Show all countries" to see and pick a different one instead. If the detected language isn't available yet, the wizard says so and shows English plus whatever languages are ready today. Languages available in the plugin catalog also appear on the language step — pick one and the till downloads and installs it right there (it needs the internet for that one download); if the download can't finish just then, the till keeps trying in the background and you can carry on in one of the ready languages meanwhile.
7. In Germany, the wizard adds a business-details step right after the country step: legal business name, owner's name, tax number (Steuernummer or USt-IdNr.) and business address. These details are used only to set up receipt signing (TSE) for your shop through Universal Till Cloud — no bank details are ever asked for. The step is optional: skip it to set signing up later or to bring your own fiscalisation. If you fill it in, the till submits the request in the background — even if it was offline during setup, it keeps retrying on its own, and Settings shows exactly where things stand (still in progress, waiting for the service to be prepared, or declined and why) until it completes or you dismiss it. Stated plainly, as it should be: for a signing service we set up this way, Universal Till — not you — holds the device's admin credential (PUK) and manages the service on your behalf; it belongs to your shop and you can request it from us at any time. If a matching tax plugin is available in the plugin catalog for your country (Germany today), this same step also offers to install it: it applies the country's dine-in/takeaway VAT rates and signs each sale with your configured TSE. It's entirely optional and never installs itself — install it now, or any time later from the plugin catalog, and once it's installed the wizard stops offering it.
8. The wizard also asks whether you're moving from another till system. Say no to start fresh, or choose CSV/Excel to go straight into the catalog importer. Not ready yet? Pick "Ask me later" and an "Import from another POS" prompt appears under Settings → Data until you use it or dismiss it.
9. The wizard's last screen also asks — once, and not ticked unless you tick it — whether to register this till with the Universal Till cloud right away. Tick it and, as soon as setup finishes, the till sends its device ID, your store name, shop region and software version to the Universal Till cloud marketplace, used for support, updates and licensing — your address is not sent. Leave it unticked and nothing is sent — the till registers only when you first use the plugin store or press Register now. Either way setup never waits on the internet: if you're offline when you tick it, setup still finishes and the till stays unregistered until you open the plugin store, press Register now, or come back online and check Settings → Till registration. You can change your answer any time from there; see [Store registration & claiming](/help/claim).
10. The setup wizard signs you in as this till's first admin — it doesn't connect the till to YOUR account online. For the online back-office (My stores, fleet view) and paid features, see [Store registration & claiming](/help/claim).
11. Creating a user, setting someone's PIN, or activating/deactivating an account needs a manager's or admin's role. Signed in as a cashier and try one of these anyway? The screen doesn't just refuse — it opens an in-place PIN prompt a manager or admin can approve right there, the same as [other manager-approval prompts](/help/elevation) elsewhere in the till.
12. Your own name in the top bar (👤) opens Change PIN; the Lock button next to it signs you straight out to the PIN pad.
13. Stuck at the sign-in screen on a fullscreen or kiosk till? The login screen has a collapsed "Locked out? Return to desktop" link: open it and enter a manager PIN to leave the till and return to the OS desktop — no sign-in needed first. It's the same manager-PIN-gated Exit to OS window action as in Settings → Display; see [Languages & display](/help/display) for what it does on each kind of till.

## Changing your own PIN

Anyone can change their own PIN, no manager needed — a manager is only required to set or reset *someone else's* PIN from Users.

1. Tap your name (👤) in the top bar to open Change PIN.
2. Enter your current PIN, then your new PIN twice.
3. Submit: you're signed out and land back on the PIN pad — sign in again with the new PIN.

A wrong current PIN counts as a failed sign-in attempt on this till, the same as a wrong PIN at the login screen — enough wrong attempts locks the pad for everyone for a short time, so don't guess repeatedly. A new PIN already in use by someone else on this till is rejected; pick a different one.

## Idle auto-lock

An unattended, signed-in till is a real risk — anyone walking past can sell, refund or open settings as whoever last signed in. The till locks itself back to the PIN pad after it sits untouched for a while, no action or transaction lost: whatever was in the basket is exactly as you left it once you (or anyone else allowed to) sign back in.

1. Set the timeout from Settings → Auto-lock: off, or 2/5/10/15/30/60 minutes — 10 minutes to start with, until someone changes it.
2. Any tap, key press or scan on the till resets the countdown — it only fires after genuinely sitting idle.
3. Changing this setting needs a manager's or admin's role, the same [manager-approval prompt](/help/elevation) as other settings changes.
4. Don't want to wait for the timeout? Use the Lock button next to your name in the top bar to lock it yourself, any time.

## Permissions matrix

A super admin can go further than assigning cashier/manager/admin roles — they can grant or revoke exactly what each role is allowed to do, action by action (refunds, void, price overrides, settings, reports, and more), from Users → Permissions. Every change is logged in the audit trail with who made it. The one thing you can never do is revoke a super admin's own access to this page — that grant is always locked on, so nobody can accidentally lock every super admin out.

## Becoming a super admin

Only an existing super admin can create or promote another one — from Users, either pick "super admin" as the role for a brand-new account, or use "Promote to super admin" next to an existing person's name. Both are logged in the audit trail, the same as any other permission-sensitive change, and take effect on that person's next sign-in.

A shop with no super admin yet (the role didn't exist before this till version) needs a one-time setup step to create the first one — ask support if Users has no super-admin option available to you yet.

## Changing or stepping back a role

Every person's row also has a role picker and a "Change role" button, next to their name, visible to admins and super admins — the general way to move anyone between cashier, manager, admin and super admin, in either direction, not just upward. Use it to step a super admin back down to admin (rather than deactivating them, which would also drop their PIN and sign-in history), or to correct a role assigned by mistake. An admin can move people freely between cashier, manager and admin, but can't grant, remove or otherwise touch anyone's super admin role — only a super admin can do that, the same restriction as creating one. A manager can't change anyone's role. You can never leave the till with no admin or no super admin at all — the last one of either is protected the same way the last super admin is protected from deactivation. Logged in the audit trail and takes effect on the person's next sign-in, the same as every other role change here.
