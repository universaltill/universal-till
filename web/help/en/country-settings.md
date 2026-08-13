---
id: country-settings
title: Country settings
section: Setting up
order: 145
summary: The default currency, tax rate and archive retention used for each country, and how to adjust them for your own shop.
keywords: [country, currency, tax, vat, retention, archive, region, defaults]
routes: [/country-settings]
---

# Country settings

Every country the till knows about comes with sensible defaults: which currency it uses, the usual tax rate, whether that tax is already included in the price, and how long archived transaction batches are kept before they can be deleted.

Your shop uses the settings for the country you chose when you first set the till up. This page is where those per-country defaults live, if you need to change them.

## How to use it

1. Open **Country settings** from the menu (manager only). Each country is listed with its currency, tax rate and archive retention.
2. Edit the values on a row and press **Save**. Changes apply to shops using that country.
3. To add somewhere that isn't listed, fill in **Add a country** with a short code of your own choosing, its currency and tax rate.
4. **Restore defaults** puts a built-in country back to the values it shipped with. A country you added yourself is removed completely by **Delete**.

## Good to know

- Tax is entered as a percentage — enter `19` for 19%. Half-percent rates like `8.5` are fine.
- **Tax included in price** means the shelf price already contains the tax, which is normal in most of Europe. Leave it off where tax is added at the till instead.
- **Archive retention** is the smallest number of days an archived batch is kept before anyone can permanently delete it. You can raise it, but not lower it below the minimum shown — that minimum protects records you may still be required to produce.
- Changing a country's defaults does not rewrite sales you have already taken. It affects what happens from that point on.
- If you are unsure what your own country requires you to keep, ask your accountant before lowering anything.
