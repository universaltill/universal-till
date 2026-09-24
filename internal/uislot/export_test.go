package uislot

import "sort"

// KnownIconNamesForTest returns the closed icon-name set, sorted, for the
// external-package mirror test in icon_names_test.go. Test-only so the set
// itself stays unexported and immutable to production code.
func KnownIconNamesForTest() []string {
	names := make([]string, 0, len(knownIconNames))
	for n := range knownIconNames {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
