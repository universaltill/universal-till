package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ut-docs#3328 (ADR-0121 §2): ParseManifest refuses any permission string
// that is not a shape core actually grants.

func TestIsKnownPermission(t *testing.T) {
	known := []string{
		// pre-existing exact names
		"storage", "events:receive", "payments:reconciliation", "tcp:*",
		// net: exact host and the public-only wildcard
		"net:erp.lan", "net:api.stripe.com", "net:*",
		// tcp: exact host:port
		"tcp:192.168.1.5:9100", "tcp:kasa.lan:4711", "tcp:[::1]:9100",
		// setting-bound forms
		"net:@setting:endpoint_url", "tcp:@setting:okc.host:okc.port",
		// <entity>:read / <entity>:write
		"sales:read", "sales:write", "inventory:read", "items:read",
		"tax_codes:read", "fiscal_register_de:read", "customers:read",
		// core-checked: HasActivePrinterCapability (internal/data)
		"devices:printer",
		// grandfathered legacy storage quota
		"storage.local.1MB", "storage.local.10MB", "storage.local.512KB", "storage.local.2GB",
		// grandfathered legacy dotted names declared by published plugin
		// repos (ut-plugin-language-*, -themes, -faq, -integration-ai,
		// -payment-*); core checks none of them
		"ui.locale", "ui.theme", "ui.page", "ai.configure", "pos.tender",
		// ADR-0121 §2
		"http:lan", "http:stream", "db:own", "blob:own", "schedule",
		"cloud:directive", "secret:write", "ui:page",
		"view:sales_by_day", "view:sales", "view:audit", "ui:slot:reports.panels", "ui:slot:admin.pages",
		// net:/tcp: hosts in every form normGrantHost canonicalises
		"net:API.Stripe.com", "net:erp.lan.", "net:münchen.example", "net:erp_lan", "net:10.0.0.5", "net:[fe80::1]",
		// net:validation:<host> (ut-docs#3226, ADR-0121 amendment 2026-10-02):
		// a narrower grant shape sharing the "net:" prefix — must not be
		// swallowed or refused by the generic net:<host> case.
		"net:validation:kassensichv-middleware.fiskaly.com", "net:validation:10.0.0.5", "net:validation:[fe80::1]",
	}
	for _, p := range known {
		if !isKnownPermission(p) {
			t.Errorf("isKnownPermission(%q) = false, want true", p)
		}
	}
	unknown := []string{
		"", " ", "fs:root", "exec:*", "storage ", "Storage",
		"net:", "net:@setting:", "net:@anything", "tcp:@setting:okc.host",
		"tcp:", "tcp:host", "tcp:host:", "tcp:host:0", "tcp:host:65536", "tcp:host:abc", "tcp::9100",
		"sales:delete", "Sales:read", "1sales:read", ":read", "sales:read:extra",
		"storage.local.MB", "storage.local.10TB", "storage.local.10mb",
		"view:", "ui:slot:", "ui:page:x", "http:*", "db:other", "secret:delete",
		"schedule:hourly", "cloud:*", "devices:usb", "ui.other", "pos.tender.x",
		// A host no grant check could ever match (independent review,
		// ut-docs#3328): wildcard patterns, a URL, host:port, a path.
		"net:*.example.com", "net:**", "net:*x", "net:https://x", "net:host:443", "net:a/b", "net:a?b", "net:-erp.lan",
		"tcp:*:9100", "tcp:*.lan:9100", "tcp:http://x:9100",
		// view:<class> is an identifier; ui:slot:<slot> is a content slot
		// (ADR-0121 §5, §7) — the sale screen has none.
		"view:foo bar", "view:a:b", "view:sales.by_day.v1", "ui:slot:checkout.sidebar", "ui:slot:x y",
		// net:validation:<host> malformed forms (ParseValidationPermission).
		"net:validation:", "net:validation:*", "net:validation:host:443", "net:validation:a/b",
	}
	for _, p := range unknown {
		if isKnownPermission(p) {
			t.Errorf("isKnownPermission(%q) = true, want false", p)
		}
	}
}

func TestParseManifestRefusesUnknownPermission(t *testing.T) {
	const head = `{"id":"com.test.perm","name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","permissions":`
	for _, p := range []string{"fs:root", "exec:*", ""} {
		src := head + `[` + strconv.Quote(p) + `]}`
		_, err := ParseManifest(strings.NewReader(src))
		if err == nil {
			t.Errorf("permission %q: manifest accepted, want refusal", p)
			continue
		}
		if !strings.Contains(err.Error(), strconv.Quote(p)) || !strings.Contains(err.Error(), "not a recognized permission") {
			t.Errorf("permission %q: error %q should name the permission and say it is not recognized", p, err)
		}
	}
	// Every ADR-0121 §2 name passes ParseManifest as a whole.
	src := head + `["http:lan","http:stream","db:own","blob:own","view:sales","ui:page","ui:slot:reports.panels","schedule","cloud:directive","secret:write"]}`
	if _, err := ParseManifest(strings.NewReader(src)); err != nil {
		t.Errorf("ADR-0121 permissions refused: %v", err)
	}
}

// The permission lists published plugin repos declared on 2026-10-02 (the
// manifests themselves live in those repos, not here). Each must keep
// passing ParseManifest, or an installed plugin's settings page would start
// refusing writes (InstalledManifest -> ParseManifest) after a core upgrade.
func TestPublishedPluginRepoPermissionsStillParse(t *testing.T) {
	const head = `{"id":"com.test.published","name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm",
	"settings":[{"key":"endpoint_url"}],"permissions":`
	lists := map[string]string{
		"ut-plugin-tax-de":              `["events:receive","sales:read","fiscal_register_de:read","net:kassensichv-middleware.fiskaly.com","storage"]`,
		"ut-plugin-tax-uk":              `["events:receive","sales:read","net:test-api.service.hmrc.gov.uk","storage"]`,
		"ut-plugin-language-*":          `["ui.locale"]`,
		"ut-plugin-theme*":              `["ui.theme"]`,
		"ut-plugin-faq":                 `["ui.page"]`,
		"ut-plugin-integration-ai":      `["ai.configure"]`,
		"ut-plugin-button-nosale":       `["events:receive"]`,
		"ut-plugin-integration-webhook": `["events:receive","net:*","net:@setting:endpoint_url","storage"]`,
		"ut-plugin-payment-stripe":      `["pos.tender","events:receive","net:api.stripe.com","storage"]`,
		"ut-plugin-payment-sumup":       `["pos.tender","events:receive","net:api.sumup.com","storage"]`,
		"ut-plugin-payment-qrpay":       `["pos.tender","events:receive"]`,
		"ut-plugin-payment-demo":        `["pos.tender","events:receive","storage"]`,
	}
	for repo, perms := range lists {
		if _, err := ParseManifest(strings.NewReader(head + perms + `}`)); err != nil {
			t.Errorf("%s: published permission list refused: %v", repo, err)
		}
	}
}

// The card's AC: every shipped manifest still installs. Globs plugins/**/plugin.json
// (so a new plugin dir is covered automatically) plus the signed marketplace
// fixture. A failure here is a bug in the allow-list, never in the manifest.
func TestEveryShippedManifestStillParses(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "plugins"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "plugin.json" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk plugins/: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("found %d plugins/**/plugin.json, want at least 2 (layout-salon, tax-tr): %v", len(files), files)
	}
	// The signed marketplace fixtures (testdata/*.json) — never edited to
	// fit the validator: their permissions are covered by the signature.
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no testdata/*.json fixtures found (want marketplace_signed_manifest.json)")
	}
	files = append(files, fixtures...)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := ParseManifest(strings.NewReader(string(b))); err != nil {
			t.Errorf("%s: shipped manifest no longer parses: %v", f, err)
		}
	}
}

// The marketplace install path verifies with VerifyManifest, which never
// goes through ParseManifest (same split as provides/markets and ABI-3) —
// it must refuse the same permissions.
func TestVerifyManifest_RefusesUnknownPermission(t *testing.T) {
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	base := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none","canonical_type":"tax","device_arch":"any","permissions":`
	p := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(p, []byte(base+`["storage","exec:*"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mv.VerifyManifest(p); err == nil || !strings.Contains(err.Error(), `"exec:*"`) {
		t.Fatalf("VerifyManifest should refuse exec:*, got %v", err)
	}
	if err := os.WriteFile(p, []byte(base+`["storage","http:lan","view:sales_by_day","storage.local.10MB"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mv.VerifyManifest(p); err != nil {
		t.Fatalf("known permissions refused: %v", err)
	}
}
