// Package shoptype is the ADR-0026 shop-type taxonomy as a dependency-free
// leaf, so the enrolment layer can validate a cloud-supplied type (ADR-0171)
// without importing internal/plugins (which imports enroll in its tests).
package shoptype

// all is the ADR-0026 taxonomy in display order. ut-cloud's pkg/manifest
// mirrors it (TestProvidesClosedSetMirrorsPOS).
var all = []string{"cafe", "retail", "service", "hospitality", "market_stall", "other"}

// List returns a copy of the shop types, in display order.
func List() []string { return append([]string(nil), all...) }

// IsKnown reports whether v is one of List.
func IsKnown(v string) bool {
	for _, t := range all {
		if v == t {
			return true
		}
	}
	return false
}
