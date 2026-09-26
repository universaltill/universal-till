package cloudsync

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
)

// Manage-shop catalog directives (ut-docs
// reference/manage-shop-catalog-api.md §3): save_item, save_category,
// delete_category, save_modifier_group, delete_modifier_group.

// mainTillOnlyTypes are skipped entirely on a satellite till: no apply and
// no result post, so the directive stays pending for the main till
// (contract §3). The older catalog types keep their own
// requirePrimaryDirective refusal; the cloud's delivery filter (§2.10)
// covers them.
var mainTillOnlyTypes = map[string]bool{
	"save_item":             true,
	"save_category":         true,
	"delete_category":       true,
	"save_modifier_group":   true,
	"delete_modifier_group": true,
	// Till user directives (reference/till-user-directives.md §4): only
	// the main till applies them; the admin bundle carries the result to
	// the other tills (ADR-0115 §1).
	"save_user":       true,
	"set_user_pin":    true,
	"deactivate_user": true,
}

// catalogTypes are the directive types that change what the catalog
// snapshot reports: after one of them applies, the same tick pushes the
// snapshot again (its hash gate keeps an unchanged push free).
var catalogTypes = map[string]bool{
	"save_item": true, "save_category": true, "delete_category": true,
	"save_modifier_group": true, "delete_modifier_group": true,
	"set_price": true, "rename_item": true, "deactivate_item": true, "create_item": true,
	"add_barcode": true, "update_item_details": true, "adjust_stock": true,
	"upsert_category": true, "update_category": true, "upsert_modifier_group": true,
}

// payload is a presence-aware reader over a directive's payload: every
// opt* method returns (nil, true) for an ABSENT key, (value, true) for a
// well-formed one and (nil, false) for a present value of the wrong shape.
type payload map[string]any

func (p payload) optStr(k string) (*string, bool) {
	v, present := p[k]
	if !present {
		return nil, true
	}
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	s = strings.TrimSpace(s)
	return &s, true
}

func (p payload) optBool(k string) (*bool, bool) {
	v, present := p[k]
	if !present {
		return nil, true
	}
	switch t := v.(type) {
	case bool:
		return &t, true
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		if err != nil {
			return nil, false
		}
		return &b, true
	}
	return nil, false
}

// optInt accepts a whole JSON number or its string form.
func (p payload) optInt(k string) (*int64, bool) {
	v, present := p[k]
	if !present {
		return nil, true
	}
	switch t := v.(type) {
	case float64:
		if t != math.Trunc(t) || math.Abs(t) > 1<<53 {
			return nil, false
		}
		n := int64(t)
		return &n, true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return nil, false
		}
		return &n, true
	}
	return nil, false
}

// optIDs decodes a JSON array of strings riding inside a string field,
// trimmed, capped at maxCategoryLinkIDs.
func (p payload) optIDs(k string) (*[]string, bool) {
	v, present := p[k]
	if !present {
		return nil, true
	}
	raw, ok := v.(string)
	if !ok {
		return nil, false
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil || arr == nil || len(arr) > maxCategoryLinkIDs {
		return nil, false
	}
	for i := range arr {
		arr[i] = strings.TrimSpace(arr[i])
	}
	return &arr, true
}

func (p payload) id() string {
	s, _ := p["id"].(string)
	return strings.TrimSpace(s)
}

// decodeSaveItem reads a save_item payload (§3.1). A non-empty msg is the
// directive's failure text.
func decodeSaveItem(p payload) (data.ItemPatch, string) {
	out := data.ItemPatch{ID: p.id()}
	if out.ID == "" {
		return out, "missing id"
	}
	var ok bool
	create, ok := p.optBool("create")
	if !ok {
		return out, "bad create"
	}
	out.Create = create != nil && *create
	for _, f := range []struct {
		k   string
		dst **string
	}{{"name", &out.Name}, {"sku", &out.SKU}, {"category_id", &out.CategoryID}, {"color", &out.Color}} {
		if *f.dst, ok = p.optStr(f.k); !ok {
			return out, "bad " + f.k
		}
	}
	if out.PriceMinor, ok = p.optInt("price_minor"); !ok {
		return out, "bad price_minor"
	}
	for _, f := range []struct {
		k   string
		dst **bool
	}{{"active", &out.Active}, {"is_weighed", &out.IsWeighed}, {"stock_untracked", &out.StockUntracked}} {
		if *f.dst, ok = p.optBool(f.k); !ok {
			return out, "bad " + f.k
		}
	}
	for _, f := range []struct {
		k   string
		dst **[]string
	}{{"barcodes", &out.Barcodes}, {"modifier_group_ids", &out.ModifierGroupIDs}, {"modifier_opt_out_ids", &out.ModifierOptOutIDs}} {
		if *f.dst, ok = p.optIDs(f.k); !ok {
			return out, "bad " + f.k
		}
	}
	return out, ""
}

// decodeSaveCategory reads a save_category payload (§3.2).
func decodeSaveCategory(p payload) (data.CategorySave, string) {
	out := data.CategorySave{ID: p.id()}
	if out.ID == "" {
		return out, "missing id"
	}
	create, ok := p.optBool("create")
	if !ok {
		return out, "bad create"
	}
	out.Create = create != nil && *create
	for _, f := range []struct {
		k   string
		dst **string
	}{{"name", &out.Name}, {"parent_id", &out.ParentID}, {"color", &out.Color}, {"icon", &out.Icon}} {
		if *f.dst, ok = p.optStr(f.k); !ok {
			return out, "bad " + f.k
		}
	}
	if out.ShowOnSaleScreen, ok = p.optBool("show_on_sale_screen"); !ok {
		return out, "bad show_on_sale_screen"
	}
	if out.GroupIDs, ok = p.optIDs("modifier_group_ids"); !ok {
		return out, "bad modifier_group_ids"
	}
	if out.StationIDs, ok = p.optIDs("station_ids"); !ok {
		return out, "bad station_ids"
	}
	return out, ""
}

// decodeSaveModifierGroup reads a save_modifier_group payload (§3.4).
func decodeSaveModifierGroup(p payload) (data.ModifierGroupSave, string) {
	out := data.ModifierGroupSave{ID: p.id()}
	if out.ID == "" {
		return out, "missing id"
	}
	create, ok := p.optBool("create")
	if !ok {
		return out, "bad create"
	}
	out.Create = create != nil && *create
	if out.Name, ok = p.optStr("name"); !ok {
		return out, "bad name"
	}
	if out.Required, ok = p.optBool("required"); !ok {
		return out, "bad required"
	}
	for _, f := range []struct {
		k   string
		dst **int
	}{{"min_select", &out.MinSelect}, {"max_select", &out.MaxSelect}} {
		n, ok := p.optInt(f.k)
		if !ok || (n != nil && (*n < math.MinInt32 || *n > math.MaxInt32)) {
			return out, "bad " + f.k
		}
		if n != nil {
			v := int(*n)
			*f.dst = &v
		}
	}
	if v, present := p["options"]; present {
		raw, isStr := v.(string)
		var decoded []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			PriceDeltaMinor int64  `json:"price_delta_minor"`
			Active          *bool  `json:"active"`
		}
		if !isStr || json.Unmarshal([]byte(raw), &decoded) != nil || decoded == nil {
			return out, "bad options"
		}
		opts := make([]data.ModifierOption, 0, len(decoded))
		for _, o := range decoded {
			active := o.Active == nil || *o.Active
			opts = append(opts, data.ModifierOption{ID: strings.TrimSpace(o.ID), Name: strings.TrimSpace(o.Name), PriceDeltaMinor: o.PriceDeltaMinor, IsActive: active})
		}
		out.Options = &opts
	}
	for _, f := range []struct {
		k   string
		dst *[]string
	}{{"attach_category_ids", &out.AttachCategoryIDs}, {"detach_category_ids", &out.DetachCategoryIDs},
		{"attach_item_ids", &out.AttachItemIDs}, {"detach_item_ids", &out.DetachItemIDs}} {
		ids, ok := p.optIDs(f.k)
		if !ok {
			return out, "bad " + f.k
		}
		if ids != nil {
			*f.dst = *ids
		}
	}
	return out, ""
}
