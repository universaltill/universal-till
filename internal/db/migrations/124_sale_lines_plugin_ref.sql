-- Opaque per-line plugin ref on a sale line (ut-docs#7206, ADR-0121 amendment
-- 2026-11-28). A page plugin that adds a line to the basket from the sell
-- screen overlay may attach a short opaque id (never personal data) plus its
-- own plugin id; core stores both and echoes them in sale.completed
-- line_items[]. NULL (the only value for every existing row) = no ref.
-- Additive: no existing reader changes. The archive twin gets the same
-- columns so a transaction-history reset keeps them.
ALTER TABLE sale_lines ADD COLUMN plugin_ref TEXT;
ALTER TABLE sale_lines ADD COLUMN plugin_ref_by TEXT;
ALTER TABLE sale_lines_archive ADD COLUMN plugin_ref TEXT;
ALTER TABLE sale_lines_archive ADD COLUMN plugin_ref_by TEXT;
