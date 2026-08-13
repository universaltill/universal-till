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

Every country the till knows about comes with sensible defaults: which currency it uses, the usual tax rate, whether that tax is already included in the price, and a minimum number of days archived transaction batches should be kept.

This page is where those per-country defaults live. **The first-boot setup wizard now reads them**, so an edit here reaches the country step the next time a till is set up from scratch. Two things are still not wired to it: it does not change a shop that has *already* been set up (its currency/tax stay whatever was chosen at the time), and it does not yet control how long archives are actually kept — archives are still kept for a fixed minimum regardless of the retention value shown here.

## How to use it

1. Open **Country settings** from the menu (manager only). Each country is listed with its currency, tax rate and archive-retention floor.
2. Edit the values on a row and press **Save**.
3. To add somewhere that isn't listed, fill in **Add a country** with a short code of your own choosing (letters and numbers only, up to 8 characters), its currency and tax rate.
4. **Restore defaults** puts a built-in country back to the values it shipped with. A country you added yourself is removed completely by **Delete**.

## Good to know

- Tax is entered as a percentage — enter `19` for 19%. Half-percent rates like `8.5` are fine and are saved exactly — though the setup wizard prefills a new till to the nearest whole percent, so `8.5` arrives there as `9` (editable afterwards in Settings, same as any other rate).
- **Tax included in price** means the shelf price already contains the tax, which is normal in most of Europe. Leave it off where tax is added at the till instead.
- **Archive retention** here is a floor you can raise but not lower below the minimum shown. It does not yet control when archives are actually deleted — today, archives are kept for a fixed minimum regardless of this setting.
- Editing a country here does not change any shop already set up, and does not rewrite sales you have already taken.
- If you are unsure what your own country requires you to keep, ask your accountant before assuming any number shown here is a compliance guarantee — this page does not certify compliance with any particular country's record-keeping law.
