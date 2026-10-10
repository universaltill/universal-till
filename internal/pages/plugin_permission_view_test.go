package pages

import (
	"html"
	"net/http/httptest"
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
		"tcp:@setting:okc.host:okc.port", "pos.tender", "view:", "ui:slot:", "ui:page:x", "device-info:*"} {
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
	items := []storeItem{
		{ListingID: "l1", Name: "Card reader", Version: "1.0", Type: "payment",
			Permissions: []string{"http:lan", "view:sales_by_day", "storage"}},
	}
	for _, loc := range []string{"en", "ar", "fa", "tr"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/plugins/store?lang="+loc, nil)
		httpx.Render("ui/pages/plugins_store.html", map[string]any{
			"title": "Plugin Store", "menuItems": nil, "Items": items, "Categories": storeCategories(items),
		})(rec, req)
		body := rec.Body.String()
		for _, key := range []string{"plugins.permissions.desc.http_lan", "plugins.permissions.desc.view"} {
			desc := html.EscapeString(httpx.T(loc, key))
			if !strings.Contains(body, ">"+desc+"<") {
				t.Errorf("%s: %s must render as visible element text %q", loc, key, desc)
			}
			if strings.Contains(body, `title="`+desc) {
				t.Errorf("%s: %s must not be a tooltip", loc, key)
			}
		}
		if got := strings.Count(body, "perm-desc-item"); got != 2 {
			t.Errorf("%s: want 2 description lines (http:lan, view:), got %d", loc, got)
		}
		// The pre-existing path is untouched: storage keeps badge + tooltip.
		if !strings.Contains(body, `title="`+html.EscapeString(httpx.T(loc, "plugins.store.permission_hint"))+`">storage</span>`) {
			t.Errorf("%s: storage badge must render exactly as before", loc)
		}
		if got := strings.Count(body, "perm-badge"); got != 3 {
			t.Errorf("%s: want 3 permission badges, got %d", loc, got)
		}
	}
}
