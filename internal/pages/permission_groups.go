package pages

import "github.com/universaltill/universal-till/internal/uislot"

// permissionGroup is one heading on /users/permissions (ut-docs#3132): a
// locale key suffix (permissions.group.<Key>) and the actions under it, in
// display order.
type permissionGroup struct {
	Key     string
	Actions []string
}

// permissionGroups orders the permission matrix for a shop owner reading it
// top to bottom. Every permission_actions row belongs to exactly one group —
// TestPermissionGroups_EveryActionInExactlyOneGroup fails when a migration
// adds an action this table doesn't place. At runtime an unplaced action
// still renders, under a trailing permissionOtherGroup group, so a
// grantable permission is never hidden. Display only: grouping changes no
// grant, no gate and no stored data.
var permissionGroups = []permissionGroup{
	{Key: "sales", Actions: []string{"void", "void_comp_waste", "refund", "price_override", "cash_adjustment", "worker_allocation", "fiscal_tse_override"}},
	{Key: "catalog", Actions: []string{"catalog_management", "tax_code_management", "import_export"}},
	{Key: "stock", Actions: []string{"stock_management", "stock_location_management"}},
	{Key: "reports", Actions: []string{"reports", "eod_report", "audit"}},
	{Key: "staff", Actions: []string{"user_management", "permission_management"}},
	{Key: "settings", Actions: []string{"settings", "sync_management", "data_management"}},
	{Key: "plugins", Actions: []string{"plugin_management"}},
	{Key: "system", Actions: []string{"issue_reporting"}},
}

// permissionOtherGroup is the runtime fallback group for an action
// permissionGroups doesn't place (reserved — never a key in the table).
const permissionOtherGroup = "other"

// menuCompositePredicates lists the menuPredicates names (menu_page.go)
// that are NOT a plain permission action, mapped to the permission action
// they are nested under — "" for a predicate derived from other entries,
// which the permission page never lists. Kept beside the group table so
// the "Unlocks" list and TestPermissionGuard_CoreVisibleIfNamesAreActionsOrComposite
// read one source: a new composite predicate on a core uislot entry fails
// that test until it is placed here.
var menuCompositePredicates = map[string]string{
	"fiscal_register_de": "settings", // settings AND the shop's country/plugin
	"fiscal_device_tr":   "settings", // settings AND the shop's country/plugin
	"administration":     "",         // derived from its children's gates
}

// menuUnlock is one core menu/rail entry an action makes visible: its
// label key and the VisibleIf name that gates it (the action itself, or a
// composite predicate nested under it).
type menuUnlock struct {
	LabelKey  string
	Predicate string
}

// coreUISlotTables is every core uislot table that can carry a VisibleIf.
func coreUISlotTables() map[string][]uislot.Entry {
	return map[string][]uislot.Entry{
		"CoreMenu":     uislot.CoreMenu,
		"CoreRail":     uislot.CoreRail,
		"CoreItems":    uislot.CoreItems,
		"CoreSettings": uislot.CoreSettings,
	}
}

// menuUnlocksByAction derives, from the core uislot tables, which menu
// tiles and rail entries each permission action makes visible — so the
// permission page's "Unlocks" list is never maintained by hand. Order:
// menu tiles, then rail, then the items/settings sub-menus; one entry per
// label per action.
func menuUnlocksByAction() map[string][]menuUnlock {
	out := map[string][]menuUnlock{}
	seen := map[string]bool{}
	tables := coreUISlotTables()
	for _, name := range []string{"CoreMenu", "CoreRail", "CoreItems", "CoreSettings"} {
		for _, e := range tables[name] {
			if e.VisibleIf == "" || e.LabelKey == "" {
				continue
			}
			action := e.VisibleIf
			if base, composite := menuCompositePredicates[e.VisibleIf]; composite {
				if base == "" {
					continue
				}
				action = base
			}
			if seen[action+"\x00"+e.LabelKey] {
				continue
			}
			seen[action+"\x00"+e.LabelKey] = true
			out[action] = append(out[action], menuUnlock{LabelKey: e.LabelKey, Predicate: e.VisibleIf})
		}
	}
	return out
}
