package pages

import (
	"html"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ADR-0121 §2 (ut-docs#3328): the ten new permission names carry a plain-
// language description key; every other permission keeps DescKey empty and
// renders exactly as before.
func TestDescribePermission_ADR0121DescKeys(t *testing.T) {
	want := map[string]string{
		"http:lan":               "plugins.permissions.desc.http_lan",
		"http:stream":            "plugins.permissions.desc.http_stream",
		"db:own":                 "plugins.permissions.desc.db_own",
		"blob:own":               "plugins.permissions.desc.blob_own",
		"view:sales_by_day":      "plugins.permissions.desc.view",
		"ui:page":                "plugins.permissions.desc.ui_page",
		"ui:slot:reports.panels": "plugins.permissions.desc.ui_slot",
		"schedule":               "plugins.permissions.desc.schedule",
		"cloud:directive":        "plugins.permissions.desc.cloud_directive",
		"secret:write":           "plugins.permissions.desc.secret_write",
		// ADR-0140 (ut-docs#3862).
		"device-info": "plugins.permissions.desc.device_info",
		// ADR-0149 §6 (ut-docs#3976): the staff list gets its own consent
		// line (exact names win over the generic view: fallback); every
		// other view class keeps the generic one.
		"view:users":     "plugins.permissions.desc.view_users",
		"view:inventory": "plugins.permissions.desc.view",
		// ut-docs#4045: the non-★ shop facts class says what it never reads.
		"view:shop": "plugins.permissions.desc.view_shop",
		// ut-docs#3515: plain, unencrypted HTTP to one host must not look
		// like an ordinary https net:<host> grant.
		"net:validation:crl.example-ca.eu": "plugins.permissions.desc.net_validation",
		"net:validation:[2001:db8::1]":     "plugins.permissions.desc.net_validation",
	}
	for perm, key := range want {
		b := describePermission(perm)
		if b.DescKey != key {
			t.Errorf("describePermission(%q).DescKey = %q, want %q", perm, b.DescKey, key)
		}
		if b.Name != perm {
			t.Errorf("describePermission(%q).Name = %q, want the raw string", perm, b.Name)
		}
		for _, loc := range []string{"en", "ar", "fa", "tr"} {
			if got := httpx.T(loc, key); got == key || strings.TrimSpace(got) == "" {
				t.Errorf("%s: missing translation for %q", loc, key)
			}
		}
	}
	for _, perm := range []string{"storage", "net:api.stripe.com", "tcp:*", "sales:read",
		"tcp:@setting:okc.host:okc.port", "pos.tender", "view:", "ui:slot:", "ui:page:x", "device-info:*",
		// A malformed net:validation: grant is refused at install
		// (ParseValidationPermission), so it never earns the description.
		"net:validation:", "net:validation:*.example.eu", "net:validation:ca.eu:80", "net:validation:http://ca.eu"} {
		if b := describePermission(perm); b.DescKey != "" {
			t.Errorf("describePermission(%q).DescKey = %q, want empty", perm, b.DescKey)
		}
	}
}

// ADR-0153 (ut-docs#2837): the AI agent permissions carry consent lines —
// one per ai.send.<class>, and one ai.tools.write line for every area (the
// raw name next to it shows the area). A malformed AI name never earns one.
func TestDescribePermission_AIAgentDescKeys(t *testing.T) {
	want := map[string]string{
		"ai.agent":                "plugins.permissions.desc.ai_agent",
		"ai.send.sales":           "plugins.permissions.desc.ai_send_sales",
		"ai.send.catalog":         "plugins.permissions.desc.ai_send_catalog",
		"ai.send.stock":           "plugins.permissions.desc.ai_send_stock",
		"ai.send.customers":       "plugins.permissions.desc.ai_send_customers",
		"ai.send.staff":           "plugins.permissions.desc.ai_send_staff",
		"ai.send.till_health":     "plugins.permissions.desc.ai_send_till_health",
		"ai.tools.write:catalog":  "plugins.permissions.desc.ai_tools_write",
		"ai.tools.write:stock":    "plugins.permissions.desc.ai_tools_write",
		"ai.tools.write:settings": "plugins.permissions.desc.ai_tools_write",
		"ai.tools.write:plugins":  "plugins.permissions.desc.ai_tools_write",
		"ai.tool.provide":         "plugins.permissions.desc.ai_tool_provide",
		"ai.tool.provide.write":   "plugins.permissions.desc.ai_tool_provide_write",
	}
	for perm, key := range want {
		if b := describePermission(perm); b.DescKey != key {
			t.Errorf("describePermission(%q).DescKey = %q, want %q", perm, b.DescKey, key)
		}
		for _, loc := range []string{"en", "ar", "fa", "tr"} {
			if got := httpx.T(loc, key); got == key || strings.TrimSpace(got) == "" {
				t.Errorf("%s: missing translation for %q", loc, key)
			}
		}
	}
	for _, perm := range []string{"ai.send.shop", "ai.send.foo", "ai.send.", "ai.tools.write:", "ai.tools.write:fiscal", "ai.configure", "ai.agent:x"} {
		if b := describePermission(perm); b.DescKey != "" {
			t.Errorf("describePermission(%q).DescKey = %q, want empty", perm, b.DescKey)
		}
	}
}

// The description is VISIBLE text, never only a title= tooltip (touchscreen
// till: nothing depends on hover). Old permissions keep their badge+tooltip.
func TestPluginStoreRendersADR0121PermissionDescriptionsAsVisibleText(t *testing.T) {
	chdirRoot(t)
	initPagesI18n(t)
	// ut-docs#4180: the permission list moved from the store card to the
	// listing's own page, verbatim.
	item := storeItem{ListingID: "l1", Name: "Card reader", Version: "1.0", Type: "payment",
		Permissions: []string{"http:lan", "view:sales_by_day", "storage", "net:validation:crl.example-ca.eu"}}
	for _, loc := range []string{"en", "ar", "fa", "tr"} {
		body := renderStoreDetail(t, item, loc)
		for _, key := range []string{"plugins.permissions.desc.http_lan", "plugins.permissions.desc.view", "plugins.permissions.desc.net_validation"} {
			desc := html.EscapeString(httpx.T(loc, key))
			if !strings.Contains(body, ">"+desc+"<") {
				t.Errorf("%s: %s must render as visible element text %q", loc, key, desc)
			}
			if strings.Contains(body, `title="`+desc) {
				t.Errorf("%s: %s must not be a tooltip", loc, key)
			}
		}
		if got := strings.Count(body, "perm-desc-item"); got != 3 {
			t.Errorf("%s: want 3 description lines (http:lan, view:, net:validation:), got %d", loc, got)
		}
		// The pre-existing path is untouched: storage keeps badge + tooltip.
		if !strings.Contains(body, `title="`+html.EscapeString(httpx.T(loc, "plugins.store.permission_hint"))+`">storage</span>`) {
			t.Errorf("%s: storage badge must render exactly as before", loc)
		}
		if got := strings.Count(body, "perm-badge"); got != 4 {
			t.Errorf("%s: want 4 permission badges, got %d", loc, got)
		}
	}
}
