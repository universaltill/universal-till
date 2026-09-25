-- 046_demo_instance.sql — ADR-0113 §1.2 (universaltill/ut-docs#2687).
-- The "this database is a demo template" flag for the public try-the-till
-- demo. At most one row (id = 1); present means demo. A real till's
-- database never has the row: nothing in the till writes it at runtime —
-- only the demo template build (and tests) call
-- DemoInstanceRepo.MarkDemoInstance.
--
-- internal/app's start gate reads it: UT_DEMO on needs the row (plus the
-- token and the paths.Data("demo") marker file), and UT_DEMO off with the
-- row present refuses to start, so a demo database can never be run as a
-- normal till.
--
-- Deliberately its own table, not a settings row: settings are writable by
-- LAN sync and cloud set_setting directives. Per-database and never synced
-- (sync_admin_repo.go's nonAdminTables).
CREATE TABLE IF NOT EXISTS demo_instance (
    id INTEGER PRIMARY KEY CHECK (id = 1)
);
