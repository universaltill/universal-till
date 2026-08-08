-- ut-docs#191 (split from #17/023's independent review): 001_init.sql's
-- shortcut_buttons seed rows carry the same class of fabricated 13th digit
-- as the item_barcodes/variant_barcodes rows 023 already fixed — a naive
-- incrementing counter instead of a real EAN-13 mod-10 weighted check
-- digit. 001 is released and append-only (same reasoning as 023), so the
-- corrected values are applied here instead of edited into the seed. Only
-- the check digit changes; the first 12 digits (and item_id/label/
-- image_path) are untouched.
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000012' WHERE barcode = '2000010000017';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000029' WHERE barcode = '2000010000024';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000036' WHERE barcode = '2000010000031';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000043' WHERE barcode = '2000010000048';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000050' WHERE barcode = '2000010000055';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000067' WHERE barcode = '2000010000062';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000074' WHERE barcode = '2000010000079';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000081' WHERE barcode = '2000010000086';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000098' WHERE barcode = '2000010000093';
UPDATE OR IGNORE shortcut_buttons SET barcode = '2000010000104' WHERE barcode = '2000010000109';
