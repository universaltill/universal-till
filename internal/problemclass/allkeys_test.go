package problemclass

// AllKeys lists (test-only) every reason/next locale key Classify can return (for the
// en.json coverage test).
func AllKeys() []string {
	var out []string
	for _, r := range knownRules {
		out = append(out, keyPrefix+string(r.id)+".reason", keyPrefix+string(r.id)+".next")
	}
	for _, a := range Areas {
		out = append(out, keyPrefix+"generic."+string(a)+".reason", keyPrefix+"generic."+string(a)+".next")
	}
	return out
}
