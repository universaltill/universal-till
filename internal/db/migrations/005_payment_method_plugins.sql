-- Payment methods contributed by plugins (plugin_entries type='payment').
-- plugin_id is NULL for built-ins; plugin-backed rows are (de)activated as
-- their plugin is enabled/disabled/uninstalled. Rows are kept (not deleted)
-- because payments.method_id references them from sales history.
ALTER TABLE payment_methods ADD COLUMN plugin_id TEXT;
