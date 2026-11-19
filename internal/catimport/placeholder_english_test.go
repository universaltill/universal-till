package catimport

// PlaceholderIcon and PlaceholderIconPath were the English-only entry
// points before ut-docs#2654. Production now goes through IconMatcher
// (a shop-language matcher per import or request), so they have no
// production caller and the whole-program deadcode guard refuses them
// (scripts/ci/guard-deadcode-baseline.sh). They stay here, test-only, so
// placeholder_test.go keeps pinning the English table's behaviour
// unchanged: a nil *IconMatcher is exactly the English-only path.

func PlaceholderIcon(name, category string) string {
	return (*IconMatcher)(nil).Icon(name, category)
}

func PlaceholderIconPath(name, category string) string {
	return (*IconMatcher)(nil).IconPath(name, category)
}
