package cloudsync

// Till user directives (ut-docs reference/till-user-directives.md §4;
// ADR-0115 §2 and its 2026-09-25 amendment): save_user, set_user_pin and
// deactivate_user. Main-till only (mainTillOnlyTypes). A PIN travels only
// as pin_sealed, HPKE-sealed to the main till's directive key
// (internal/directivekey); this package never opens it.

// UserDirective is a decoded user directive. Pointer fields are nil when
// the key was absent (keep); Active is only ever nil or true. DirectiveID,
// Type and the store's external id make up the seal's AAD; CreatedBy is the
// cloud's queuing actor for the audit provenance ("" when the cloud did not
// send one).
type UserDirective struct {
	DirectiveID string
	Type        string
	CreatedBy   string
	UserID      string
	Create      bool
	Username    *string
	DisplayName *string
	Role        *string
	Active      *bool
	PINSealed   *string
}

// decodeUserDirective reads one of the three user directive payloads. A
// non-empty msg is the directive's failure text.
func decodeUserDirective(d directive) (UserDirective, string) {
	p := payload(d.Payload)
	out := UserDirective{DirectiveID: d.ID, Type: d.Type, CreatedBy: d.CreatedBy}
	if id, ok := p.optStr("user_id"); ok && id != nil {
		out.UserID = *id
	}
	if out.UserID == "" {
		return out, "missing user_id"
	}
	sealed, ok := p.optStr("pin_sealed")
	if !ok || (sealed != nil && *sealed == "") {
		if d.Type == "set_user_pin" {
			return out, "missing pin_sealed"
		}
		return out, "bad pin_sealed"
	}
	switch d.Type {
	case "set_user_pin":
		if sealed == nil {
			return out, "missing pin_sealed"
		}
		out.PINSealed = sealed
	case "deactivate_user":
		// user_id only.
	case "save_user":
		out.PINSealed = sealed
		create, ok := p.optBool("create")
		if !ok {
			return out, "bad create"
		}
		out.Create = create != nil && *create
		for _, f := range []struct {
			k   string
			dst **string
		}{{"username", &out.Username}, {"display_name", &out.DisplayName}, {"role", &out.Role}} {
			if *f.dst, ok = p.optStr(f.k); !ok {
				return out, "bad " + f.k
			}
		}
		// Only reactivation rides save_user; deactivation is its own type.
		active, ok := p.optBool("active")
		if !ok || (active != nil && !*active) {
			return out, "bad active"
		}
		out.Active = active
		if !out.Create && out.Username == nil && out.DisplayName == nil && out.Role == nil && out.Active == nil && out.PINSealed == nil {
			return out, "nothing to update"
		}
	}
	return out, ""
}
