// Command seed_card_readers_mixed (ut-docs#6566 Tester) seeds TWO card
// terminal test methods into a throwaway till: "e2esumup" with 3 readers and
// "e2estripe" with 1, all backed by the readers_guest wasm (state driven by
// the reader id's text). UT_E2E_EXTRA_READERS=N adds N more ready readers on
// e2esumup (scroll test). Run from the repo root with UT_DATA_DIR set.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/db"
)

type prov struct{ pluginID, name, key string }

var provs = []prov{
	{"com.example.e2e-sumup", "Sample SumUp-like terminals", "e2esumup"},
	{"com.example.e2e-stripe", "Sample Stripe-like terminals", "e2estripe"},
}

type rd struct{ id, name, key, ref string }

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" {
		fmt.Fprintln(os.Stderr, "UT_DATA_DIR must be set")
		os.Exit(2)
	}
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	tx, _ := conn.Begin()
	ex := func(q string, a ...any) {
		if _, err := tx.Exec(q, a...); err != nil {
			_ = tx.Rollback()
			panic(fmt.Sprint(err, q))
		}
	}
	for _, p := range provs {
		modDir := filepath.Join(dataDir, "plugins", p.pluginID, "1.0.0")
		_ = os.MkdirAll(modDir, 0o755)
		b := exec.Command("go", "build", "-o", filepath.Join(modDir, "plugin.wasm"), "./internal/plugins/testdata/readers_guest")
		b.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if out, err := b.CombinedOutput(); err != nil {
			panic(string(out))
		}
		ex(`INSERT OR IGNORE INTO plugin_catalog (id, version, name, description, author, website, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?, datetime('now'))`,
			p.pluginID, "1.0.0", p.name, "", "", "", "wasm", "./plugin.wasm", "local-e2e-fixture", strings.Repeat("0", 64), "1.0.0", "1.0")
		ex(`INSERT OR IGNORE INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?,?,?,?,?,1)`, p.pluginID, p.name, "1.0.0", "./plugin.wasm", "wasm")
		ex(`INSERT OR IGNORE INTO plugin_entries (id, plugin_id, type, key, label, trigger_event, is_active, sort_order) VALUES (?,?,?,?,?,?,1,0)`,
			p.key+"-entry", p.pluginID, "payment", p.key, p.name, "payment."+p.key+".requested")
		ex(`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?,?,?,1)`, p.key+"-ev", p.pluginID, "events:receive")
		for _, ev := range []string{"authorize", "refund", "readers"} {
			ex(`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?,?,?,?,1)`, p.key+"-h-"+ev, p.pluginID, "payment."+p.key+"."+ev, ev)
		}
	}
	rows := []rd{
		{"e2e-r1", "Front counter", "e2esumup", "rdr_front"},
		{"e2e-r2", "Back office", "e2esumup", "rdr_back"},
		{"e2e-r3", "Bar", "e2esumup", "rdr_offline_bar"},
		{"e2e-r4", "Pop-up stand", "e2estripe", "tmr_popup"},
	}
	extra, _ := strconv.Atoi(os.Getenv("UT_E2E_EXTRA_READERS"))
	for i := 0; i < extra; i++ {
		rows = append(rows, rd{fmt.Sprintf("e2e-x%d", i), fmt.Sprintf("Extra reader %d", i+1), "e2esumup", fmt.Sprintf("rdr_extra%d", i)})
	}
	for i, r := range rows {
		pid := provs[0].pluginID
		if r.key == "e2estripe" {
			pid = provs[1].pluginID
		}
		ex(`INSERT OR IGNORE INTO card_readers (id, name, method_key, plugin_id, reader_ref, kind, source, sort_order) VALUES (?,?,?,?,?,?,?,?)`, r.id, r.name, r.key, pid, r.ref, "cloud", "manual", i)
	}
	if err := tx.Commit(); err != nil {
		panic(err)
	}
	fmt.Println("seeded mixed readers")
}
