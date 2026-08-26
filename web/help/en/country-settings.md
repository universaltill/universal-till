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

Every country the till knows about comes with sensible defaults: which currency it uses, the usual tax rate, whether that tax is already included in the price, and a minimum number of days archived transaction batches should be kept. Each country also has its own default language (for example, Germany defaults to German) — this part isn't editable from this page yet, only currency, tax and retention are.

This page is where those per-country defaults live. **The first-boot setup wizard now reads them**, so an edit here reaches the country step the next time a till is set up from scratch. It does not change a shop that has *already* been set up — its currency/tax stay whatever was chosen at the time. Archive retention is different: the value shown here is what a permanent-delete of a reset archive batch (Settings → Data) is measured against, for every shop, the moment it's saved — see the Reports help topic's "Report retention" section for how that plays out.

## How to use it

1. Open **Country settings** from the menu (manager only). By default you see only your own shop's country, with its currency, tax rate and archive-retention floor.
2. Edit the values on the row and press **Save**.
3. To see every country the till knows about — useful if you're setting values up for a till that will run in a different country — follow **Show all countries**. **Show only my country** takes you back to just your own.
4. To add somewhere that isn't listed, fill in **Add a country** with a short code of your own choosing (letters and numbers only, up to 8 characters), its currency and tax rate — this form is available in both views.
5. **Restore defaults** puts a built-in country back to the values it shipped with. A country you added yourself is removed completely by **Delete**.

## Good to know

- Tax is entered as a percentage — enter `19` for 19%. Half-percent rates like `8.5` are fine and are saved exactly — though the setup wizard prefills a new till to the nearest whole percent, so `8.5` arrives there as `9` (editable afterwards in Settings, same as any other rate).
- **Tax included in price** means the shelf price already contains the tax, which is normal in most of Europe. Leave it off where tax is added at the till instead.
- **Archive retention** here is a floor you can raise but not lower below the minimum shown. It controls when a reset archive batch (Settings → Data → Reset archives) becomes eligible for permanent deletion: a batch holding real sales can't be deleted until this many days have passed since it was archived. Raising the value protects existing batches further out immediately; it never shortens protection already in effect. The Reset archives list itself shows each protected batch's retained-until date directly and hides its Delete-permanently button until then, so you never type the confirmation only to be refused.
- Editing a country here does not change any shop already set up, and does not rewrite sales you have already taken.
- Choosing a country at first-boot setup also sets the till's default language to that country's own (e.g. Germany → German) — it does not require the matching language pack to already be installed. Changing your shop's country later, via the raw key/value editor under Settings → All Settings (`store.country`), does the same thing automatically **only when that language is already installed** or reads left-to-right the same as English (e.g. French, Spanish); a right-to-left language like Arabic is left as it is until its pack is installed, so you never lose a language you can already read for one whose text and layout aren't ready yet.
- If you are unsure what your own country requires you to keep, ask your accountant before assuming any number shown here is a compliance guarantee — this page does not certify compliance with any particular country's record-keeping law.
- If your shop's own country setting doesn't match anything on this page (rare — this normally can't happen), the page shows every country with a note explaining why, instead of the usual single-country view.
