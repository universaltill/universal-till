-- 009: related-items co-occurrence table.
-- AI increment 2 (docs: architecture/ai-integration.md §h): "customers also
-- buy" suggestions come from the shop's OWN sales history, precomputed into
-- this table by a background rebuild. Reads at sale time are a local indexed
-- lookup — zero network in the checkout path (ADR-0003).
CREATE TABLE IF NOT EXISTS related_items (
    item_id         TEXT NOT NULL,
    related_item_id TEXT NOT NULL,
    support         INTEGER NOT NULL,   -- completed baskets containing both items
    score           REAL NOT NULL,      -- support² / (baskets_a × baskets_b): cosine²
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (item_id, related_item_id),
    FOREIGN KEY (item_id)         REFERENCES items (id) ON DELETE CASCADE,
    FOREIGN KEY (related_item_id) REFERENCES items (id) ON DELETE CASCADE
);
