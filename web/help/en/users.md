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
6. The wizard's language and country steps start pre-filled from this device's own system language and timezone — no internet lookup, so it works before the till is even online — and both stay freely changeable with one tap. If the detected language isn't available yet, the wizard says so and shows English plus whatever languages are ready today.
7. The wizard also asks whether you're moving from another till system. Say no to start fresh, or choose CSV/Excel to go straight into the catalog importer. Not ready yet? Pick "Ask me later" and an "Import from another POS" prompt appears under Settings → Data until you use it or dismiss it.

## Permissions matrix

A super admin can go further than assigning cashier/manager/admin roles — they can grant or revoke exactly what each role is allowed to do, action by action (refunds, void, price overrides, settings, reports, and more), from Users → Permissions. Every change is logged in the audit trail with who made it. The one thing you can never do is revoke a super admin's own access to this page — that grant is always locked on, so nobody can accidentally lock every super admin out.

## Becoming a super admin

Only an existing super admin can create or promote another one — from Users, either pick "super admin" as the role for a brand-new account, or use "Promote to super admin" next to an existing person's name. Both are logged in the audit trail, the same as any other permission-sensitive change, and take effect on that person's next sign-in.

A shop with no super admin yet (the role didn't exist before this till version) needs a one-time setup step to create the first one — ask support if Users has no super-admin option available to you yet.
