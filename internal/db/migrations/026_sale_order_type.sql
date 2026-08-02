-- Order type (dine-in vs. takeaway) was tracked in-memory during checkout
-- (pos.Service.orderType) but discarded at sale completion, so a past
-- sale's receipt/journal/kitchen ticket could never show whether it was
-- dine-in or takeaway. '' = dine-in/standard, matching the existing
-- pos.OrderTypeTakeaway convention (no new enum needed).
ALTER TABLE sales ADD COLUMN order_type TEXT NOT NULL DEFAULT '';
