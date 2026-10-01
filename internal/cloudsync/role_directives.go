package cloudsync

import (
	"encoding/json"
	"strings"
)

// Custom role directives (ADR-0128 §1/§3, ut-docs#3165): save_role and
// delete_role. Main-till only (mainTillOnlyTypes); the admin bundle carries
// the result to the other tills. This package only decodes the shape; the
// key format, label rules and grant checks are the hook's
// (pages.cloudSaveRole / cloudDeleteRole).

// maxRoleGrants caps the decoded grant list — far above the till's action
// count, so it only stops a pathological payload.
const maxRoleGrants = 1000

// RoleDirective is a decoded role directive. Grants is the complete,
// de-duplicated set of granted action names for save_role (never nil there;
// empty = a role that may do nothing) and nil for delete_role. CreatedBy is
// the cloud's queuing actor for the audit provenance.
type RoleDirective struct {
	DirectiveID string
	Type        string
	CreatedBy   string
	Role        string
	Label       string
	Grants      []string
	Create      bool
}

// decodeRoleDirective reads a save_role or delete_role payload. A non-empty
// msg is the directive's failure text.
func decodeRoleDirective(d directive) (RoleDirective, string) {
	p := payload(d.Payload)
	out := RoleDirective{DirectiveID: d.ID, Type: d.Type, CreatedBy: d.CreatedBy}
	if role, ok := p.optStr("role"); ok && role != nil {
		out.Role = *role
	}
	if out.Role == "" {
		return out, "missing role"
	}
	if d.Type == "delete_role" {
		return out, ""
	}
	label, ok := p.optStr("label")
	if !ok {
		return out, "bad label"
	}
	if label == nil {
		return out, "missing label"
	}
	out.Label = *label
	v, present := p["grants"]
	if !present {
		return out, "missing grants"
	}
	raw, ok := v.(string)
	if !ok {
		return out, "bad grants"
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil || arr == nil || len(arr) > maxRoleGrants {
		return out, "bad grants"
	}
	seen := make(map[string]bool, len(arr))
	out.Grants = make([]string, 0, len(arr))
	for _, a := range arr {
		a = strings.TrimSpace(a)
		if a == "" {
			return out, "bad grants"
		}
		if !seen[a] {
			seen[a] = true
			out.Grants = append(out.Grants, a)
		}
	}
	create, ok := p.optBool("create")
	if !ok {
		return out, "bad create"
	}
	out.Create = create != nil && *create
	return out, ""
}
