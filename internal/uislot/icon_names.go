package uislot

// knownIconNames is the closed drawn-icon-name set Decision H's "a NAME in
// core's built-in icon set (internal/httpx/icons.go)" refers to. The actual
// SVG bodies stay in httpx (only the render path needs them, and httpx
// already imports internal/plugins for its self-update badge helpers, so
// internal/plugins can never import internal/httpx back without a cycle).
// This package's own doc comment above already commits to depending on
// nothing else in the product specifically so both sides of a mechanism can
// import it — icon_names_test.go (TestKnownIconNamesMirrorsHttpxIconNames)
// pins that this set and httpx's railIcons keys never drift apart, the same
// same-repo "must mirror" + guard-test shape ut-cloud's
// internal/signing.CanonicalManifest / internal/claims.CategoryColors use
// across repos (ut-docs#1734).
//
// internal/plugins.validatePageEntryIcon is the first consumer that needed
// membership-checking rather than just naming an icon (a `layout` plugin's
// Decision H re-icon amendment is deliberately NOT validated against this
// set at parse time — httpx.Icon's bounded lookup already makes an unknown
// amended name safe by construction, falling back through IconFallback then
// genericFallbackIcon); a plugin's own declared page-entry default fails
// loudly at install instead.
//
// Unexported (review of ut-docs#1734): it gates install-time validation, so
// no other package may mutate it at runtime — read it through
// IsKnownIconName.
var knownIconNames = map[string]bool{
	"receipt": true, "menu": true, "package": true, "bell": true, "help": true,
	"pulse": true, "bug": true, "users": true, "tag": true, "percent": true,
	"globe": true, "user": true, "lock": true, "sync": true, "fiscal": true,
	"bluetooth": true, "puzzle": true, "scissors": true, "palette": true,
	"clock": true, "book-open": true, "chart-column": true, "settings": true,
	"map-pin": true, "calculator": true, "chef-hat": true,
	"utensils-crossed": true, "flag": true, "monitor": true,
	"clipboard-list": true, "recycle": true, "shopping-cart": true,
	"trash-2": true, "plus": true, "pencil": true, "x": true, "search": true,
	"chevron-up": true, "chevron-down": true, "check": true, "camera": true,
	"arrow-up": true, "sparkles": true, "upload": true, "download": true,
	"landmark": true, "scan-barcode": true, "log-out": true, "filter": true,
	"arrow-left": true, "chevron-left": true, "chevron-right": true,
	"pause": true, "keyboard": true, // ut-docs#2702 compact tender row
	"rotate-ccw": true, "eye": true, "ellipsis": true, "eye-off": true,
}

// IsKnownIconName reports whether name is in the closed icon-name set.
func IsKnownIconName(name string) bool { return knownIconNames[name] }
