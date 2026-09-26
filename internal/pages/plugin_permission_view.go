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
}

func describePermission(name string) permissionBadge {
	b := permissionBadge{Name: name}
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
