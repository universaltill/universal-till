-- ut-docs#17: 001_init.sql seeded item_barcodes/variant_barcodes with a
-- fabricated 13th digit (a naive incrementing counter) instead of a real
-- EAN-13 mod-10 weighted check digit, so a printed scanner-test sheet had
-- to render as Code128 (an EAN-mode scanner refuses an invalid check
-- digit). 001 is released and append-only, so the corrected values are
-- applied here instead of edited into the seed. Only the check digit
-- changes; item_id/variant_id/barcode_type/is_primary are untouched.
UPDATE item_barcodes SET barcode = '5000000000012' WHERE barcode = '5000000000011';
UPDATE item_barcodes SET barcode = '5000000000029' WHERE barcode = '5000000000028';
UPDATE item_barcodes SET barcode = '5000000000036' WHERE barcode = '5000000000035';
UPDATE item_barcodes SET barcode = '5000000000043' WHERE barcode = '5000000000042';
UPDATE item_barcodes SET barcode = '5000000000050' WHERE barcode = '5000000000059';
UPDATE item_barcodes SET barcode = '5000000000067' WHERE barcode = '5000000000066';
UPDATE item_barcodes SET barcode = '5000000000074' WHERE barcode = '5000000000073';
UPDATE item_barcodes SET barcode = '5000000000081' WHERE barcode = '5000000000080';
UPDATE item_barcodes SET barcode = '5000000000098' WHERE barcode = '5000000000097';
UPDATE item_barcodes SET barcode = '5000000000104' WHERE barcode = '5000000000103';
UPDATE item_barcodes SET barcode = '5000000000111' WHERE barcode = '5000000000110';
UPDATE item_barcodes SET barcode = '5000000000128' WHERE barcode = '5000000000127';
UPDATE item_barcodes SET barcode = '5000000000135' WHERE barcode = '5000000000134';
UPDATE item_barcodes SET barcode = '5000000000142' WHERE barcode = '5000000000141';
UPDATE item_barcodes SET barcode = '5000000000159' WHERE barcode = '5000000000158';
UPDATE item_barcodes SET barcode = '5000000000166' WHERE barcode = '5000000000165';
UPDATE item_barcodes SET barcode = '5000000000173' WHERE barcode = '5000000000172';
UPDATE item_barcodes SET barcode = '5000000000180' WHERE barcode = '5000000000189';
UPDATE item_barcodes SET barcode = '5000000000197' WHERE barcode = '5000000000196';
UPDATE item_barcodes SET barcode = '5000000000203' WHERE barcode = '5000000000202';
UPDATE item_barcodes SET barcode = '5000000000210' WHERE barcode = '5000000000219';
UPDATE item_barcodes SET barcode = '5000000000227' WHERE barcode = '5000000000226';
UPDATE item_barcodes SET barcode = '5000000000234' WHERE barcode = '5000000000233';
UPDATE item_barcodes SET barcode = '5000000000241' WHERE barcode = '5000000000240';
UPDATE item_barcodes SET barcode = '5000000000258' WHERE barcode = '5000000000257';
UPDATE item_barcodes SET barcode = '5000000000265' WHERE barcode = '5000000000264';
UPDATE item_barcodes SET barcode = '5000000000272' WHERE barcode = '5000000000271';
UPDATE item_barcodes SET barcode = '5000000000289' WHERE barcode = '5000000000288';
UPDATE item_barcodes SET barcode = '5000000000296' WHERE barcode = '5000000000295';
UPDATE item_barcodes SET barcode = '5000000000302' WHERE barcode = '5000000000301';
UPDATE item_barcodes SET barcode = '5000000000319' WHERE barcode = '5000000000318';
UPDATE item_barcodes SET barcode = '5000000000326' WHERE barcode = '5000000000325';
UPDATE item_barcodes SET barcode = '5000000000333' WHERE barcode = '5000000000332';
UPDATE item_barcodes SET barcode = '5000000000340' WHERE barcode = '5000000000349';
UPDATE item_barcodes SET barcode = '5000000000357' WHERE barcode = '5000000000356';
UPDATE item_barcodes SET barcode = '5000000000364' WHERE barcode = '5000000000363';
UPDATE item_barcodes SET barcode = '5000000000371' WHERE barcode = '5000000000370';
UPDATE item_barcodes SET barcode = '5000000000388' WHERE barcode = '5000000000387';
UPDATE item_barcodes SET barcode = '5000000000395' WHERE barcode = '5000000000394';
UPDATE item_barcodes SET barcode = '5000000000401' WHERE barcode = '5000000000400';
UPDATE item_barcodes SET barcode = '5000000000418' WHERE barcode = '5000000000417';
UPDATE item_barcodes SET barcode = '5000000000425' WHERE barcode = '5000000000424';
UPDATE item_barcodes SET barcode = '5000000000432' WHERE barcode = '5000000000431';
UPDATE item_barcodes SET barcode = '5000000000449' WHERE barcode = '5000000000448';
UPDATE item_barcodes SET barcode = '5000000000456' WHERE barcode = '5000000000455';
UPDATE item_barcodes SET barcode = '5000000000463' WHERE barcode = '5000000000462';
UPDATE item_barcodes SET barcode = '5000000000470' WHERE barcode = '5000000000479';
UPDATE item_barcodes SET barcode = '5000000000487' WHERE barcode = '5000000000486';
UPDATE item_barcodes SET barcode = '5000000000494' WHERE barcode = '5000000000493';
UPDATE item_barcodes SET barcode = '5000000000500' WHERE barcode = '5000000000509';

UPDATE variant_barcodes SET barcode = '6000000000011' WHERE barcode = '6000000000018';
UPDATE variant_barcodes SET barcode = '6000000000028' WHERE barcode = '6000000000025';
UPDATE variant_barcodes SET barcode = '6000000000035' WHERE barcode = '6000000000032';
UPDATE variant_barcodes SET barcode = '6000000000042' WHERE barcode = '6000000000049';
UPDATE variant_barcodes SET barcode = '6000000000059' WHERE barcode = '6000000000056';
UPDATE variant_barcodes SET barcode = '6000000000066' WHERE barcode = '6000000000063';
UPDATE variant_barcodes SET barcode = '6000000000073' WHERE barcode = '6000000000070';
UPDATE variant_barcodes SET barcode = '6000000000080' WHERE barcode = '6000000000087';
UPDATE variant_barcodes SET barcode = '6000000000097' WHERE barcode = '6000000000094';
UPDATE variant_barcodes SET barcode = '6000000000103' WHERE barcode = '6000000000100';
UPDATE variant_barcodes SET barcode = '6000000000110' WHERE barcode = '6000000000117';
UPDATE variant_barcodes SET barcode = '6000000000127' WHERE barcode = '6000000000124';
