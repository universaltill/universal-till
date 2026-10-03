// Package catalogsync is the catalogue write-through shared by both sides
// (ut-docs#2817): the route table that says which catalogue mutations
// travel to the main till and which record each one edits, the wire types,
// and the additional-till client (Forward).
//
// It lives in its own package because both internal/pages (categories,
// quick-sale buttons, the Designer, and the main-till endpoint
// sync_catalog.go) and internal/pages/catalog (items, variants, barcodes,
// modifier groups) use it, and pages already imports catalog — a helper in
// pages would be an import cycle.
//
// The shape mirrors the shop-wide settings write-through (ut-docs#2791,
// settings_sync_proxy.go / sync_settings.go): an additional till never
// writes a catalogue change locally — the admin bundle is main-till-wins,
// so a local write would be reverted by the next pull. It sends the change
// to the main till's POST /api/sync/catalog/apply instead. Unlike settings,
// a catalogue save is not a key/value batch but one editor save of one
// record, with each screen's own validation and side effects, so what
// travels is the save itself — the route and its form fields — and the main
// till runs ITS OWN handler for that route (the same code a save made on
// the main till runs: one write path, never a second one), after deciding
// the actor's permission with its own users and roles and running the
// optimistic conflict check. The handler's answer (the same row fragment,
// redirect or trigger a local save answers with) is relayed back to the
// operator unchanged; the main till's link nudge then brings the change to
// every till, this one included.
package catalogsync

import (
	"net/url"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
)

// ApplyPath is the main till's write-through endpoint.
const ApplyPath = "/api/sync/catalog/apply"

// Route is one catalogue mutation that travels to the main till.
type Route struct {
	// Pattern is the ServeMux pattern, with its method, matching the route
	// as the handler is mounted.
	Pattern string
	// Kind is the parent record the save edits (data.CatalogKind*).
	Kind string
	// IDFrom names where the record's id is: "path:<name>" for a path
	// value, "form:<a>,<b>" for the first non-empty of these form fields,
	// "" when the save names no single existing record (a create, a
	// reorder, a bulk change).
	IDFrom string
	// Conflict marks a save that edits an existing record's own fields:
	// only these carry base_updated_at and can be refused as a conflict.
	// Creates, deactivations, deletes, reorders and child rows (barcodes,
	// modifier options, links) are applied as asked.
	Conflict bool
}

// Routes is the complete list (ut-docs#2817's scope: items, variants and
// their barcodes, categories, quick-sale buttons, modifier groups and their
// options). Photo uploads are NOT here and never travel: item/category
// photos are distributed by the asset ledger, a separate binary path, so a
// photo upload on an additional till stays refused (Forward refuses any
// request carrying a file). Option sets, kitchen routing and the bulk
// barcode/SKU backfills are outside this card and keep their
// main-till-only refusal.
var Routes = []Route{
	// Items (internal/pages/catalog/handlers.go).
	{Pattern: "POST /api/catalog/item", Kind: data.CatalogKindItem},
	{Pattern: "POST /api/catalog/item/update", Kind: data.CatalogKindItem, IDFrom: "form:id", Conflict: true},
	{Pattern: "POST /api/catalog/item/deactivate", Kind: data.CatalogKindItem, IDFrom: "form:id"},
	{Pattern: "POST /api/catalog/item-cost", Kind: data.CatalogKindItem, IDFrom: "form:panelItem", Conflict: true},
	{Pattern: "POST /api/catalog/item-lead-time", Kind: data.CatalogKindItem, IDFrom: "form:panelItem", Conflict: true},
	{Pattern: "POST /api/catalog/item-reorder-level", Kind: data.CatalogKindItem, IDFrom: "form:panelItem", Conflict: true},
	// Variants and barcodes (barcodes are children: no conflict check).
	{Pattern: "POST /api/catalog/variant", Kind: data.CatalogKindVariant, IDFrom: "form:id", Conflict: true},
	{Pattern: "POST /api/catalog/variant/deactivate", Kind: data.CatalogKindVariant, IDFrom: "form:id"},
	{Pattern: "POST /api/catalog/barcode", Kind: data.CatalogKindItem, IDFrom: "form:itemId,panelItem"},
	{Pattern: "POST /api/catalog/barcode/delete", Kind: data.CatalogKindItem, IDFrom: "form:panelItem"},
	// Modifier groups and their options/links.
	{Pattern: "POST /api/catalog/modifier-group", Kind: data.CatalogKindModifierGroup, IDFrom: "form:id", Conflict: true},
	{Pattern: "POST /api/catalog/modifier-option", Kind: data.CatalogKindModifierGroup, IDFrom: "form:groupId"},
	{Pattern: "POST /api/catalog/modifier-group/attach", Kind: data.CatalogKindModifierGroup, IDFrom: "form:groupId"},
	{Pattern: "POST /api/catalog/modifier-group/detach", Kind: data.CatalogKindModifierGroup, IDFrom: "form:groupId"},
	{Pattern: "POST /api/catalog/modifier-group/delete", Kind: data.CatalogKindModifierGroup, IDFrom: "form:groupId"},
	{Pattern: "POST /api/catalog/modifier-group/attach-category", Kind: data.CatalogKindModifierGroup, IDFrom: "form:groupId"},
	{Pattern: "POST /api/catalog/modifier-group/detach-category", Kind: data.CatalogKindModifierGroup, IDFrom: "form:groupId"},
	// Categories (categories_page.go and the Designer's
	// designer_categories_api.go).
	{Pattern: "POST /api/categories", Kind: data.CatalogKindCategory},
	{Pattern: "POST /api/categories/reorder", Kind: data.CatalogKindCategory},
	{Pattern: "POST /api/categories/{id}", Kind: data.CatalogKindCategory, IDFrom: "path:id", Conflict: true},
	{Pattern: "POST /api/categories/{id}/active", Kind: data.CatalogKindCategory, IDFrom: "path:id"},
	{Pattern: "POST /api/designer/categories", Kind: data.CatalogKindCategory},
	{Pattern: "POST /api/designer/categories/reorder", Kind: data.CatalogKindCategory},
	{Pattern: "POST /api/designer/categories/{id}", Kind: data.CatalogKindCategory, IDFrom: "path:id", Conflict: true},
	{Pattern: "POST /api/designer/categories/{id}/active", Kind: data.CatalogKindCategory, IDFrom: "path:id"},
	// Quick-sale buttons (buttons_api.go). /api/buttons/add re-labels the
	// item's existing button row whatever code is posted
	// (ShortcutsRepo.AddButton), so it is conflict-checked on THAT row,
	// found by item id (ut-docs#3606; the posted code named no row and
	// skipped the check).
	{Pattern: "POST /api/buttons/reorder", Kind: data.CatalogKindButton},
	{Pattern: "POST /api/buttons/recategorize", Kind: data.CatalogKindItem, IDFrom: "form:item_id,itemId"},
	{Pattern: "POST /api/buttons/add", Kind: data.CatalogKindItemButton, IDFrom: "form:itemId,item_id", Conflict: true},
	{Pattern: "POST /api/buttons/remove", Kind: data.CatalogKindButton, IDFrom: "form:code"},
	{Pattern: "POST /api/buttons/hide", Kind: data.CatalogKindItem, IDFrom: "form:item_id,itemId"},
	{Pattern: "POST /api/buttons/unhide", Kind: data.CatalogKindItem, IDFrom: "form:item_id,itemId"},
	{Pattern: "POST /api/buttons/unhide-all", Kind: data.CatalogKindItem},
	{Pattern: "POST /api/buttons/remove-from-grid", Kind: data.CatalogKindItem, IDFrom: "form:item_id,itemId"},
	{Pattern: "POST /api/buttons/delete-item", Kind: data.CatalogKindItem, IDFrom: "form:item_id,itemId"},
}

// Resolve finds the route for method+path and the id of the record it
// edits ("" when it names none). ok=false when the route is not one that
// travels. Matching follows ServeMux's rules for the shapes Routes uses: the
// method must match, a literal segment matches itself, a {name} segment
// matches one non-empty segment, and a route with no wildcard wins over one
// with a wildcard ("/api/categories/reorder" before "/api/categories/{id}").
// (Not a ServeMux itself: a mux here would read as a second registration of
// these routes to the demo-mode route scan.)
func Resolve(method, path string, form url.Values) (Route, string, bool) {
	segs := strings.Split(path, "/")
	var (
		best     Route
		bestVals map[string]string
		found    bool
	)
	for _, rt := range Routes {
		vals, ok := matchPattern(rt.Pattern, method, segs)
		if !ok {
			continue
		}
		if !found || (len(bestVals) > 0 && len(vals) == 0) {
			best, bestVals, found = rt, vals, true
		}
	}
	if !found {
		return Route{}, "", false
	}
	id := ""
	if name, isPath := strings.CutPrefix(best.IDFrom, "path:"); isPath {
		id = bestVals[name]
	}
	if names, isForm := strings.CutPrefix(best.IDFrom, "form:"); isForm {
		for _, n := range strings.Split(names, ",") {
			if v := strings.TrimSpace(form.Get(n)); v != "" {
				id = v
				break
			}
		}
	}
	return best, id, true
}

// matchPattern matches one "METHOD /a/{b}/c" pattern against method and the
// request path's segments, returning the wildcard values.
func matchPattern(pattern, method string, segs []string) (map[string]string, bool) {
	pm, pp, ok := strings.Cut(pattern, " ")
	if !ok || pm != method {
		return nil, false
	}
	psegs := strings.Split(pp, "/")
	if len(psegs) != len(segs) {
		return nil, false
	}
	var vals map[string]string
	for i, ps := range psegs {
		if strings.HasPrefix(ps, "{") && strings.HasSuffix(ps, "}") {
			if segs[i] == "" {
				return nil, false
			}
			if vals == nil {
				vals = map[string]string{}
			}
			vals[ps[1:len(ps)-1]] = segs[i]
			continue
		}
		if ps != segs[i] {
			return nil, false
		}
	}
	return vals, true
}

// ApplyRequest is the POST body of /api/sync/catalog/apply: one editor save.
type ApplyRequest struct {
	// Method, Path and Query name the save's route on the main till.
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query,omitempty"`
	// Form is the save's posted fields, as the operator submitted them
	// (minus override_pin: an elevation is decided on this till and only
	// the approver's id travels).
	Form url.Values `json:"form"`
	// Headers carries the htmx request headers the handlers branch on
	// (HX-Request and friends); nothing else from the browser travels.
	Headers map[string]string `json:"headers,omitempty"`
	// Locale is the operator's locale, so the answered fragment renders in
	// the language the operator is using.
	Locale string `json:"locale,omitempty"`
	// BaseUpdatedAt is the record's updated_at as this till's editor saw
	// it; set only for a Conflict route editing an existing record.
	BaseUpdatedAt string `json:"base_updated_at,omitempty"`
	ActorID       string `json:"actor_id"`
	ApproverID    string `json:"approver_id,omitempty"`
}

// ApplyAnswer is the success data: the main till's own handler response,
// relayed to the operator unchanged.
type ApplyAnswer struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
	// Entity, EntityID and UpdatedAt name the record the save edited and
	// its updated_at after the save (when it names one that exists).
	Entity    string `json:"entity,omitempty"`
	EntityID  string `json:"entity_id,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// WireError is the error object of a refused write-through.
type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Refusal codes the main till answers with besides the shared ones
// (unauthorized, replica, invalid_body, unknown_actor, unknown_approver,
// forbidden, server_error).
const (
	// CodeConflict: the record changed on the main till since this till's
	// editor loaded it; nothing was written.
	CodeConflict = "conflict"
	// CodeNotSupported: the route is not one that travels.
	CodeNotSupported = "not_supported_via_sync"
	// CodePhoto: the save carried a photo upload, which never travels.
	CodePhoto = "photo_not_supported"
)

// ForwardedRequestHeaders are the request headers relayed to the main
// till's handler. ForwardedResponseHeaders are the answer headers relayed
// back to the operator. Both are allow-lists: cookies, auth and anything
// else never cross.
var (
	ForwardedRequestHeaders = []string{
		"HX-Request", "HX-Target", "HX-Trigger", "HX-Trigger-Name",
		"HX-Current-URL", "HX-Boosted",
	}
	ForwardedResponseHeaders = []string{
		"Content-Type", "Location",
		"HX-Trigger", "HX-Trigger-After-Swap", "HX-Trigger-After-Settle",
		"HX-Redirect", "HX-Refresh", "HX-Location", "HX-Reswap",
		"HX-Retarget", "HX-Reselect", "HX-Push-Url", "HX-Replace-Url",
	}
)
