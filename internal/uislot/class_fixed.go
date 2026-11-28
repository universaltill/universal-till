package uislot

// Row kinds a class is declared for. The strings match the shop-template
// schema's section names (ADR-0190).
const (
	KindSettings = "settings"
	KindMenu     = "menu"
	KindItems    = "items"
	KindRail     = "rail"
)

// FixedMenu, FixedRail, FixedItems and FixedSettings are the rows whose class
// a shop-type template can never change (ADR-0190 decision 3): statutory and
// protected pages, the restore surface for hidden tiles, payments, backup and
// the shop identity cards. They are literals on purpose: a loader test fails
// when a template names one, and TestFixedClassTablesCoverProtectedKeys fails
// if a Protected* key ever drops out of them.
var FixedMenu = []string{
	"/admin", "/fiscal-register", "/fiscal-device", "/journal",
	"/settings", "/plugins", "/help", "/report-issue",
}

// FixedRail: the rail is not demotable in v1, so every rail row is fixed.
var FixedRail = []string{"/", "/menu", "/inventory", "/orders"}

// FixedItems: the Items rows are not demotable in v1, so every row is fixed.
var FixedItems = []string{
	"/catalog", "/categories", "/inventory", "/modifiers", "/catalog/option-sets",
}

// FixedSettings: the protected data cards plus the cards that must stay
// reachable for every shop type.
var FixedSettings = []string{
	"settings-data", "settings-retention", "settings-all",
	"settings-issuereport", "settings-menulayout", "settings-payments",
	"settings-backup", "settings-store-name", "settings-currency",
	"settings-language", "settings-shop-type",
}

// FixedClassFor reports whether (kind, key) has a fixed class. An unknown
// kind is never fixed.
func FixedClassFor(kind, key string) bool {
	var list []string
	switch kind {
	case KindSettings:
		list = FixedSettings
	case KindMenu:
		list = FixedMenu
	case KindItems:
		list = FixedItems
	case KindRail:
		list = FixedRail
	default:
		return false
	}
	return IsProtectedKey(list, key)
}
