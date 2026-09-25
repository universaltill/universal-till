package iconid

import "sort"

// Test-only views of the registry: production renders through AssetPath,
// so these live here to keep the whole-program deadcode baseline clean.

// Registered reports whether this till has artwork for id.
func Registered(id string) bool {
	_, ok := registry[id]
	return ok
}

// Known lists every registered id, sorted.
func Known() []string {
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
