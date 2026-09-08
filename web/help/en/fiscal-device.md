---
id: fiscal-device
title: Fiscal device (Türkiye)
section: Setting up your shop
order: 113
summary: "See which cash register (YN ÖKC) the till pays through, whether it has proven it prints, and the last receipt it issued — the device prints the legal receipt, the till keeps its number on every sale."
routes: [/fiscal-device]
keywords: [fiscal, turkey, türkiye, ÖKC, yazarkasa, cash register, GİB, receipt, device, Z report]
---

# Fiscal device (Türkiye)

In Türkiye a retail sale is documented by a certified cash register — a *Yeni Nesil Ödeme Kaydedici Cihaz* (YN ÖKC), the "yazarkasa POS" on the counter. The device takes the money, prints the legal receipt (*mali fiş*) and reports its daily totals to the tax office through its maker. Universal Till does not replace it: with the Türkiye fiscal-device plugin installed, the device becomes a payment method called **Yazarkasa (ÖKC)**. At tender the till hands the basket to the device, the device takes cash or card and prints, and the till records the device's receipt number, serial and Z counter on the sale and shows them on its own receipt copy.

This page shows that arrangement from the till's side. It never talks to the device itself and it does not check anything with the tax office; it reads what the till recorded from the device's own answers.

## How to use it

1. Install and enable the plugin **Türkiye fiscal device (YN ÖKC)** from **Plugins** and grant it the network permission it asks for. **Fiscal device** then appears on the menu (manager only, once your shop's country is Türkiye).
2. Under **Plugin**, follow **Open plugin settings** and enter where the device is on your shop's network — the driver, address and port. Until a maker's driver is complete, the *bridge* driver talks to a bridge program or to the simulator used for testing; the plugin refuses every tender rather than pretending when its driver is not ready.
3. Take a sale with **Yazarkasa (ÖKC)** as the payment. The device prints; the till records the receipt. The first receipt marks the device as **confirmed** on this page automatically.
4. If you have already watched the device print a test receipt and want to mark it confirmed before the first real sale, press **Confirm device**. Press **Unpair device** when the device is removed or replaced — sales as system of record are refused again until a device proves itself.

## Good to know

- **Confirmed** is what the till's Türkiye safeguard reads: while the shop is set as the system of record and no device is confirmed, the till refuses to complete a sale rather than take one without a receipt. In shadow mode (the shop's existing device stays the legal record) nothing is refused.
- The device prints one fiscal receipt per sale, so the ÖKC payment must cover the whole sale; a split between the device and another method is refused.
- If the device declines, times out or cannot be reached, the tender is refused and the basket is kept — fix the device or the network and try again. Nothing is recorded on either side for a refused tender.
- **Receipts today** counts device receipts since your business-day start, the same boundary the reports use.
- This page records data and shows state. Whether your shop's device, taxpayer class and paperwork meet your obligations is between you and your accountant (*mali müşavir*); the page does not certify that.
