package catalogpull

import (
	"encoding/json"
	"sort"
	"strings"
)

// The fields the cloud's change upload accepts (contract
// cloud-catalog-changes.md 1.2.0 "Accepted fields", ut-docs#6088).
//
// A cloud rejects a whole upload call (400 invalid_request, nothing
// recorded) when one change names a field outside its list, so a till that
// knows more fields than its cloud must not send them: the cloud advertises
// its lists in the sync answer's entitlement block, the till caches them
// (internal/entitlement CatalogChangesFields) and cloudsync passes them in
// Options.AcceptedFields. No advertisement means the 1.0.0 list.

// fields100 is contract cloud-catalog-changes 1.0.0's field list per kind:
// what every cloud accepts. FROZEN: never edit it; a field a later contract
// minor adds goes into uploadFields only (TestFields100Frozen). A var only so
// a test can narrow it to stand in for a field that is not 1.0.0 yet.
var fields100 = map[string][]string{
	kindItem:     {"name", "description", "sku", "price_minor", "category_cloud_id", "tax_code_cloud_id", "active", "barcodes"},
	kindCategory: {"name", "parent_cloud_id", "sort_order", "active"},
	kindTaxCode:  {"name", "rate_basis_points", "takeaway_rate_basis_points", "active"},
}

// fieldPairs are fields that upload only together (contract 1.1.0: a net
// quantity's value and unit). One half of a pair is never sent.
var fieldPairs = map[string][][2]string{
	kindItem: {{"net_quantity_value", "net_quantity_unit"}},
}

// acceptedSet is the upload fields a cloud takes, per kind.
type acceptedSet struct {
	ok       map[string]map[string]bool // kind → accepted field
	withheld map[string][]string        // kind → uploadFields not accepted, in upload order
	fp       string                     // fingerprint of ok: "kind=f,f;kind=…", sorted
}

// acceptedFrom builds the accepted set from the cached advertisement (the
// canonical JSON {"item":["name",…],…}, "" = none). It never fails and never
// widens past what this till knows:
//
//	ok[kind] = uploadFields[kind] ∩ (fields100[kind] ∪ advertised[kind])
//
// then a pair not wholly in ok leaves it. Garbled input reads as no
// advertisement (1.0.0); a kind the advertisement lacks gets 1.0.0 too;
// advertised fields the till does not know are ignored.
func acceptedFrom(raw string) acceptedSet {
	var adv map[string][]string
	if raw != "" && json.Unmarshal([]byte(raw), &adv) != nil {
		adv = nil
	}
	a := acceptedSet{ok: map[string]map[string]bool{}, withheld: map[string][]string{}}
	kinds := make([]string, 0, len(uploadFields))
	for kind := range uploadFields {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	var fp []string
	for _, kind := range kinds {
		allowed := map[string]bool{}
		for _, f := range fields100[kind] {
			allowed[f] = true
		}
		for _, f := range adv[kind] {
			allowed[f] = true
		}
		ok := map[string]bool{}
		for _, f := range uploadFields[kind] {
			if allowed[f] {
				ok[f] = true
			}
		}
		for _, p := range fieldPairs[kind] {
			if !(ok[p[0]] && ok[p[1]]) {
				delete(ok, p[0])
				delete(ok, p[1])
			}
		}
		a.ok[kind] = ok
		var names []string
		for _, f := range uploadFields[kind] {
			if ok[f] {
				names = append(names, f)
			} else {
				a.withheld[kind] = append(a.withheld[kind], f)
			}
		}
		sort.Strings(names)
		fp = append(fp, kind+"="+strings.Join(names, ","))
	}
	a.fp = strings.Join(fp, ";")
	return a
}

// accepts reports whether the cloud takes field of kind.
func (a acceptedSet) accepts(kind, field string) bool { return a.ok[kind][field] }

// withheldOf are kind's upload fields the cloud does not take, in upload order.
func (a acceptedSet) withheldOf(kind string) []string { return a.withheld[kind] }

// anyWithheldOf reports whether kind has a withheld field.
func (a acceptedSet) anyWithheldOf(kind string) bool { return len(a.withheld[kind]) > 0 }

// anyWithheld reports whether any kind has a withheld field.
func (a acceptedSet) anyWithheld() bool {
	for _, w := range a.withheld {
		if len(w) > 0 {
			return true
		}
	}
	return false
}

// strip removes every field the cloud does not take from ch's changed_fields,
// before and after, and reports whether it removed any.
func (a acceptedSet) strip(ch *Change) (stripped bool) {
	if len(a.withheld[ch.Kind]) == 0 {
		return false
	}
	kept := []string{}
	for _, f := range ch.ChangedFields {
		if a.accepts(ch.Kind, f) {
			kept = append(kept, f)
		} else {
			stripped = true
		}
	}
	ch.ChangedFields = kept
	for _, m := range []map[string]json.RawMessage{ch.Before, ch.After} {
		for f := range m {
			if !a.accepts(ch.Kind, f) {
				delete(m, f)
				stripped = true
			}
		}
	}
	return stripped
}

// split divides fields (a kind's changed fields) into the accepted ones and
// the withheld ones, keeping their order.
func (a acceptedSet) split(kind string, fields []string) (accepted, withheld []string) {
	for _, f := range fields {
		if a.accepts(kind, f) {
			accepted = append(accepted, f)
		} else {
			withheld = append(withheld, f)
		}
	}
	return accepted, withheld
}
