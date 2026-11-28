# locale-render-audit fix (PR #2790, ut-docs#7173)

Cause: tree keys settings.tills_tree.{this,others,add,readers,kiosk} were new keys whose English values also belong to base keys, so the new-key carve-out did not apply and the de overlay had no German.

- Others / Add a till / Card readers: back to base keys tills.tab.{others,add,readers} (en.json + en.meta.json as on origin/main); deleted settings.tills_tree.{others,add,readers}. Updated settings.html and uislot/slot.go.
- This till / Kiosk: no clean base fit (role_label is a "This till: <role>" prefix; window_mode_kiosk is a display mode). Kept the new keys, added both exact strings to e2e/tests-i18n-audit/allowlist.json (German via Weblate, ADR-0160).
- Checks: guard-i18n.sh, guard-i18n-meta.sh pass; go test ./internal/pages ./internal/httpx ./internal/uislot -run 'Tills|Settings|Nav' ok; node --test e2e/tests-i18n-audit/lib/*_test.js 12/12. Full go test and CI audit not run locally.
- Skills scrum-master/dev were not loadable in this session (Unknown skill); work done directly.
