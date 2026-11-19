---
id: self-order
title: Self-order
section: Everyday selling
order: 40
public: true
summary: A customer-facing kiosk where shoppers browse by category, build their own order and pay at the kiosk or at the counter.
routes: [/self-order, /self-order/shop, /self-order/language]
keywords: [self order, self-order, kiosk, self service, customer, device profile, idle reset, pay at counter, place order, order number, exit kiosk, kiosk payment mode, table QR, language]
---

# Self-order

Self-order turns a till into a customer-facing kiosk: shoppers browse your items, build their own order and either pay at the kiosk or send the order to the counter. The screen is for customers, so it has no "?" link back to this manual. Find this topic through the manual's search, or from the help link on the **Kiosk payment mode** card in Settings.

## Which shops it suits

Self-order fits counter-service cafés, bakeries and quick-service shops, where a customer orders at a screen and collects at the counter. Guests ordering from their own phone at a table use the table QR variant of the same screens; see [Tables & floor plan](/help/tables).

## Set up a kiosk

1. Open Settings → **Look & feel** → **Display** → **Device profile** and choose **Self-order kiosk**. Only a manager can change it, and choosing it signs that screen out. The till stays a kiosk after a restart.
   - Alternative: give the **Self-order kiosk** user a PIN on the Users page and sign in with it. An additional joined till refuses that PIN. Use **Device profile** there instead: it asks you to confirm that the till becomes a satellite, and needs the main till to be reachable; see [Multiple tills](/help/multitill).
2. Open Settings → **Tills & devices** → **Kiosk payment mode** and choose **Card / contactless (kiosk)** (the default) or **Pay at counter**. It needs a manager. On a joined till this is a shop-wide setting.
3. Open Settings → **Tills & devices** → **Kiosk idle reset** and pick **Off**, 30, **60** (the default), 120 or 180 **seconds**. On a joined till this is a shop-wide setting.
4. Set the screen size (the 70% to 130% picker at the top of the card) and **Window mode** in Settings → **Look & feel** → **Display**. The window mode **Kiosk** (a locked full-screen window) is a different thing from the **Self-order kiosk** device profile: the profile decides what the till shows, the window mode decides how the window behaves. See [Languages & display](/help/display).
5. To offer more languages, install a language pack under Settings → **Language**. Picking a downloaded language there also makes it the till's own language, so set the till's language back afterwards. Every installed language appears on the start screen.
6. Card payments need a payment method and a terminal; see [Payments](/help/payments). For receipts see [Printing](/help/printing).

## What the customer sees

1. **Start screen.** The shop name once the shop is named (until then no name is shown), "**Welcome!**" and **Tap to start**. When more than one language is installed, the languages are listed by their own names (English, Deutsch, Türkçe, العربية…), with no flags.

2. **Language.** A customer who taps a language starts their order in it: buttons, headings and totals follow, and Arabic and Farsi read right to left. Item names still show in your own words. The order is saved in the shop's language, so the kitchen ticket, journal and reports don't change. The receipt the customer takes away prints in their language, whether it prints by itself at the kiosk or a cashier taps Print after taking payment for a pay-at-counter order, unless your receipt printer can't print that language's letters; then it prints in the shop's language. A reprint from the journal is the shop's copy. When the order ends or the kiosk resets, the start screen is back in the shop's language.
3. **Browse.** Chips along the top show **All** and one chip per category. There is no search box on this screen, by design. Tap an item to add it.

   ![The self-order shop screen with two items in the cart](img/self-order-shop.png)

4. **Options.** An item with variants or modifiers opens a picker. A variant choice is headed "**Choose a variant**"; a modifier group shows its own name. A * marks a required choice. The buttons are **Cancel** and **Add to cart**.

   ![The options picker open on an item with pack sizes](img/self-order-options.png)

5. **Cart.** Each line has − and + for the quantity and ✕ to remove it, and shows the item's photo or icon, the same picture as its tile. The cart carries a **Dine in** / **Takeaway** toggle (or "Table X" for a table QR); the toggle is hidden if your shop doesn't ask dine in or takeaway; this is recorded on the receipt, journal and kitchen ticket, and changes the tax only if a tax plugin for your region uses it. Below are **Subtotal**, **Service Charge** (only when you set one, or a plugin adds a levy), **Tax** and **Total**. The total is exactly what is charged, the same as a sale at the till. An empty cart says "Your cart is empty — tap an item to add it."
6. **Pay at the kiosk.** Tap **Checkout** and the kiosk shows "**Choose a payment method**". Every active payment method except cash is offered (card, gift card, voucher…). Tap **Cancel** or a method.

   ![The kiosk's payment method picker](img/self-order-checkout.png)

7. **Confirmation.** "**Order placed!**" with the **Order number**, a plain number such as 12, and **Done**. The kiosk returns to the start screen by itself after about 20 seconds. There is no tracking QR yet; see [Customer order tracking](/help/customer-order-tracking).
8. **Left alone.** After the idle reset time with no touch, the kiosk goes back to the start screen, the basket is cleared and the language returns to the shop's. A table-QR phone goes back to its own table and keeps its basket.

## Pay at the counter

With **Pay at counter**, the cart button reads **Place order**, and the dialog "**Pay at the counter**" shows the total and a **Place order** button. The kiosk takes no payment. It shows the customer an order number such as C-12, and the order waits in **Open orders** until a cashier brings it up and takes payment. It becomes a normal sale then. The kitchen ticket prints at payment for walk-up orders, and at once for a table-QR dine-in order. See [Open orders](/help/open-orders).

![The pay-at-the-counter dialog](img/self-order-counter.png)

## Getting out of kiosk mode (staff)

1. Tap 🔒. It is on the start screen, the shop screen and inside every kiosk dialog. Enter a PIN.
2. Another user's PIN puts the till back to Register and opens the sale screen.
3. On a satellite, a manager's PIN opens Settings and the kiosk stays (tap 🔒 to hand it back to customers). Other PINs are refused with a message saying the till is a satellite.
4. In a locked full-screen window, use "**Locked out? Return to desktop**" on the sign-in screen, or **Exit to OS window** in Display settings. See [Users, PINs & shifts](/help/users) and [Languages & display](/help/display).

## What can go wrong

Each line is the message the customer sees, what causes it and what to do.

- "No payment methods are available right now — please pay at the counter." No payment method other than cash is active. Set one up under [Payments](/help/payments), or switch to **Pay at counter**.
- "Payment was declined — please try again or choose another method." The terminal or provider declined the payment. The customer retries or picks another method.
- "Something went wrong completing your order — please try again." The order could not be finished. Retry; if it repeats, ask the customer to order at the counter.
- The age-restricted message ("This order includes an age-restricted item. Please pay at the counter…"). The kiosk cannot check ID, so staff take that order. See [Age-restricted sales](/help/age-restricted-sales).
- "Some items can't be ordered here right now — please pay at the counter." The tax plugin for your region is unavailable. Staff take the order at the till.
- "Ordering here is unavailable right now — please order at the counter.", "Sorry, this order can't be completed right now. Please see the counter." and "Payment couldn't be confirmed — please pay at the counter." These come from the fiscal setup. Check the fiscal device or plugin and take the order at the counter.
- "This table is already in use" and "We can’t start a new order right now", each with **Try again**. Someone else is using that table's phone, or the till is busy with many orders. Wait a minute and tap **Try again**.
- "No items found." The catalogue or the chosen category is empty. Gift vouchers are never listed on the kiosk, and inactive items don't show.
- A receipt in the shop's language instead of the customer's: your receipt printer can't print that language's letters.
- The kiosk resets while someone is still ordering: the idle reset time is too short. Raise it under **Kiosk idle reset**.
- A double tap or rapid repeats at checkout are refused on purpose, so one order is never placed twice.
- No network: the sale still completes. **Pay at counter** needs no network at all; card payment at the kiosk depends on your payment terminal.
