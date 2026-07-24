-- 018: seed a "kiosk" user (ADR-0020, spec 011 Phase 2) — self-order kiosk
-- routes are auth-exempt (anonymous walk-up customers can't PIN-login), so
-- sales completed there attribute to this fixed, well-known user instead of
-- a session (there isn't one). role='cashier' (lowest privilege) so it can
-- never pass an isManagerOrAuthOff check if ever probed. No pin_hash — this
-- user can never log in through /login either.
INSERT OR IGNORE INTO users (id, username, display_name, role, is_active)
VALUES ('kiosk', 'kiosk', 'Self-order kiosk', 'cashier', 1);
