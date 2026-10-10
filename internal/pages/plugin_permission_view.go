package pages

import (
	"strings"

	"github.com/universaltill/universal-till/internal/plugins"
)

// permissionBadge is how a manifest permission is shown to an operator
// (plugin store card, plugin settings page). A setting-bound grant
// (net:@setting:<urlKey>, tcp:@setting:<hostKey>:<portKey>, ut-docs#2899)
// is rendered in words — "connects only to the address saved in: <keys>" —
// because it is the one permission whose reach an operator decides later,
// by editing those settings. Name stays the raw string: grant/revoke act on
// it, and it is shown as a hint.
type permissionBadge struct {
	Name        string
	SettingKeys string // "okc.host, okc.port"; "" = not setting-bound
	// Target is the address a setting-bound grant currently unlocks
	// ("127.0.0.1:4711", "erp.lan"); "" = not set. Filled only where the
	// till's own settings are at hand (the plugin settings page), never on
	// a store card for a plugin that is not installed.
	Target string
	// DescKey is the i18n key of a plain-language description of what the
	// permission lets the plugin do, rendered as visible text (never only a
	// tooltip — touchscreen till). Set for the ADR-0121 §2 names only; ""
	// renders exactly as before.
	DescKey string
}

// permissionDescKeys maps the ADR-0121 §2 exact names — and view:users
// (ADR-0149 §6) and view:shop (ut-docs#4045), whose consent lines name
// what they never read — to their description.
var permissionDescKeys = map[string]string{
	"http:lan":        "plugins.permissions.desc.http_lan",
	"http:stream":     "plugins.permissions.desc.http_stream",
	"db:own":          "plugins.permissions.desc.db_own",
	"blob:own":        "plugins.permissions.desc.blob_own",
	"ui:page":         "plugins.permissions.desc.ui_page",
	"schedule":        "plugins.permissions.desc.schedule",
	"cloud:directive": "plugins.permissions.desc.cloud_directive",
	"secret:write":    "plugins.permissions.desc.secret_write",
	"device-info":     "plugins.permissions.desc.device_info", // ADR-0140
	// ADR-0149 §6 (ut-docs#3976): the staff list says what it never reads.
	"view:users": "plugins.permissions.desc.view_users",
	// ut-docs#4045: the non-★ shop facts class (shop.context.v2) says it
	// never reads sales, staff or customers.
	"view:shop": "plugins.permissions.desc.view_shop",
}

// permissionDescKey is the DescKey for name: an exact ADR-0121 §2 name, or
// the parameterised view:<name> / ui:slot:<slot> (the raw name, shown next
// to the description, carries the parameter).
func permissionDescKey(name string) string {
	if k, ok := permissionDescKeys[name]; ok {
		return k
	}
	switch {
	case strings.HasPrefix(name, "view:") && len(name) > len("view:"):
		return "plugins.permissions.desc.view"
	case strings.HasPrefix(name, "ui:slot:") && len(name) > len("ui:slot:"):
		return "plugins.permissions.desc.ui_slot"
	}
	return ""
}

func describePermission(name string) permissionBadge {
	b := permissionBadge{Name: name, DescKey: permissionDescKey(name)}
	if keys, bound, err := plugins.ParseSettingBoundPermission(name); bound && err == nil {
		b.SettingKeys = strings.Join(keys, ", ")
	}
	return b
}

// PermissionBadges is the store card's view of Permissions.
func (s storeItem) PermissionBadges() []permissionBadge {
	out := make([]permissionBadge, 0, len(s.Permissions))
	for _, p := range s.Permissions {
		out = append(out, describePermission(p))
	}
	return out
}

// PermissionDescriptions is the subset of PermissionBadges that carry a
// plain-language description, listed as visible text under the badges.
func (s storeItem) PermissionDescriptions() []permissionBadge {
	var out []permissionBadge
	for _, b := range s.PermissionBadges() {
		if b.DescKey != "" {
			out = append(out, b)
		}
	}
	return out
}
