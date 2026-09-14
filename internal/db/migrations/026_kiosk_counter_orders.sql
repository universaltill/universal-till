-- 026_kiosk_counter_orders.sql — universaltill/ut-docs#582 "Self-order
-- kiosk: 'pay at counter' mode". When kiosk.payment_mode is "counter" the
-- kiosk checkout never creates a sale/payment at all -- there's nothing to
-- charge yet, the customer pays a human at the counter after ordering.
-- This table is the record of THAT order (what to hand the customer, what
-- the kitchen should make) -- deliberately NOT a sale: no money/price
-- columns, no FK to sales, never touched by day-close/report aggregation.
--
-- lines_json is a JSON array of {name, qty, modifiers[]}. qty is a raw
-- number (internal/data/kiosk_counter_orders_repo.go's KioskCounterOrderLine
-- -- ut-docs#2221): this one stored quantity feeds a kitchen ticket print
-- (which must stay Latin digits -- an ESC/POS printer can't render
-- Arabic-Indic glyphs) and the staff-facing on-screen "pay at counter"
-- board (which should follow the viewing operator's locale), so it can't
-- be pre-formatted for either at write time -- each reader formats it
-- itself. Before ut-docs#2221, qty was a pre-formatted string instead
-- (the same convention print.KitchenItem.Qty still uses); a row an
-- already-open counter order left behind across that change still has
-- qty as a JSON string, so KioskCounterOrderLine.UnmarshalJSON accepts
-- both shapes rather than failing ListOpen on a legacy row.
--
-- display_no is a short, staff-callable reference -- deliberately its OWN
-- "C-"-prefixed sequence (internal/data/kiosk_counter_orders_repo.go),
-- never sharing sales' display_no sequence (014_sale_display_no.sql /
-- POSRepo.NextDisplayNo): staff must never be able to mistake a counter-
-- order reference for a paid receipt/display number, and a shared or even
-- similarly-shaped sequence risks exactly that confusion at the counter.
-- IF NOT EXISTS throughout, same reason as 021/023/025: this repo's
-- fiscal_signing_keys_{rename,split}_test.go rewinds the ledger and
-- re-runs every later migration against an already-migrated file.
CREATE TABLE IF NOT EXISTS kiosk_counter_orders (
    id TEXT PRIMARY KEY,
    display_no TEXT NOT NULL,
    order_type TEXT NOT NULL DEFAULT '',
    lines_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    created_at TEXT NOT NULL,
    collected_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_kiosk_counter_orders_status ON kiosk_counter_orders(status);
