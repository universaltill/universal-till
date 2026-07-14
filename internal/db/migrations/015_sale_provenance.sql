-- Which till a sale was made on (ADR-0011 D3). '' = this till (or
-- pre-sync history); a till id = journaled in from that replica.
ALTER TABLE sales ADD COLUMN till_id TEXT NOT NULL DEFAULT '';
