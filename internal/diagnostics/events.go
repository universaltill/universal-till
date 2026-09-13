// Package diagnostics is the till-side half of ADR-0092's persistent
// diagnostic mode (ut-docs#2169): an allowlisted, typed emitter of
// structured technical events, a bounded in-memory ring feeding a bounded
// on-disk pending-batch queue, and the local session state (activated by a
// Universal Till-issued code, stopped locally by a manager, or revoked by
// the cloud) that gates all of it.
//
// The load-bearing invariant (ADR-0092 §2), enforced by construction and
// by TestAllowlistedEvents_FieldTypesAreClosed: every field of every event
// struct is an opaque id, a closed enum, a bounded count/duration, or a
// bool — never free text, never an operator-typed label, never raw plugin
// response bytes. A table/order/route event carries only the id; a viewer
// that needs the human label resolves it through its own store-data
// access, not from here. Existing logging.* call sites are untouched and
// still feed ADR-0034's separate passive digest.
//
// Nothing in this package ever touches the network, and nothing here sits
// on the checkout path beyond Emit's mutex-guarded slice append: disk and
// upload work happens only from the cloudsync tick (ADR-0003).
package diagnostics

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Event is implemented by every allowlisted event struct. eventType is the
// wire discriminator ("type") and must be one of ut-cloud's
// diagnostics.AllowedEventTypes — the two repos are kept in step by
// TestAllowlistedEvents_FieldTypesAreClosed's cloudAllowedTypes mirror.
type Event interface {
	eventType() string
}

// Closed vocabularies. Every `diag:"enum"` field's allowed values are
// registered in enumValues below; Emit refuses an event whose enum field
// carries anything else, so a value outside these lists cannot reach the
// wire even through a misuse of the typed API.
const (
	// PluginAsk.Outcome — the category of what the plugin answered, never
	// the answer itself.
	OutcomeAnswer    = "answer"
	OutcomeNoOpinion = "no_opinion"
	OutcomeError     = "error"
	OutcomeMalformed = "malformed"

	// TaxProvenance.From/To — where a tax code's takeaway rate comes from.
	ProvenanceActiveOverride    = "active_override"
	ProvenanceCatalogSuggestion = "catalog_suggestion"
	ProvenanceAbsent            = "absent"

	// TableAssignment.Action / Outcome.
	TableActionClaim    = "claim"
	TableActionRelease  = "release"
	TableOutcomeClaimed = "claimed"
	TableOutcomeRefused = "refused"
	TableOutcomeError   = "error"

	// Via — which till applied a write (shared by table and order events).
	ViaPrimary = "primary"
	ViaLocal   = "local"

	// Environment.DisplayMode — the device profile (display.mode setting).
	DisplayModeRegister   = "register"
	DisplayModeBackoffice = "backoffice"
	DisplayModeSelfOrder  = "self_order"

	// PluginState.State — the plugins.install_state lifecycle value, folded
	// to a closed set (an empty/unknown row value maps to "unknown").
	PluginStateActive    = "active"
	PluginStateInstalled = "installed"
	PluginStateBroken    = "broken"
	PluginStateRevoked   = "revoked"
	PluginStateFailed    = "failed"
	PluginStateUnknown   = "unknown"
)

// PluginAsk is one plugin ask lifecycle event (a blocking EventBus.Ask/
// AskPlugin hook such as tax.rate.ask): who was asked, whether the answer
// came from the per-generation cache, how long it took, and the OUTCOME
// CATEGORY — never the payload or the response bytes.
//
// CorrelationID is unique per emitted event today — a fresh id generated
// for this one ask attempt, not (yet) a stable id threaded across
// retries of what a viewer would consider "the same logical ask." A
// caller that retries an ask (e.g. pos.recomputeTotals' optimistic retry
// on lock contention) currently produces two unrelated PluginAsk events
// with two unrelated correlation ids, which a live-tail viewer cannot
// join back together (review finding, ut-docs#2169) — tracked as a
// follow-up, since fixing it means threading a stable id down from the
// retry loop in internal/pos, not a change local to this package.
type PluginAsk struct {
	Event         string `json:"event" diag:"id"`
	PluginID      string `json:"plugin_id" diag:"id"`
	PluginVersion string `json:"plugin_version" diag:"id"`
	CacheHit      bool   `json:"cache_hit" diag:"bool"`
	Generation    uint64 `json:"generation" diag:"count"`
	DurationMS    int64  `json:"duration_ms" diag:"ms"`
	CorrelationID string `json:"correlation_id" diag:"id"`
	Outcome       string `json:"outcome" diag:"enum"`
}

// TaxProvenance records one tax code's takeaway-rate source changing
// (ADR-0073 / ut-docs#1370: active_override ↔ catalog_suggestion ↔ absent).
// Only the tax code id travels — never its name or the rate's label.
type TaxProvenance struct {
	TaxCodeID string `json:"tax_code_id" diag:"id"`
	From      string `json:"from" diag:"enum"`
	To        string `json:"to" diag:"enum"`
}

// TableAssignment is one table claim/release outcome. Table ID only —
// the table's operator-typed label is exactly the free text ADR-0092 §2
// forbids.
type TableAssignment struct {
	TableID string `json:"table_id" diag:"id"`
	Action  string `json:"action" diag:"enum"`
	Outcome string `json:"outcome" diag:"enum"`
	Via     string `json:"via" diag:"enum"`
}

// OrderStatus is one order lifecycle status change (ut-docs#526's one-tap
// board). OrderID is the receipt number; Status is pos's closed status set.
type OrderStatus struct {
	OrderID string `json:"order_id" diag:"id"`
	Status  string `json:"status" diag:"enum"`
	Applied bool   `json:"applied" diag:"bool"`
	Via     string `json:"via" diag:"enum"`
}

// Environment is the inventory-style app/build/OS/device/till record,
// emitted once per activation and once per boot while active — not tied to
// any single operator action.
type Environment struct {
	AppVersion  string `json:"app_version" diag:"id"`
	OS          string `json:"os" diag:"id"`
	Arch        string `json:"arch" diag:"id"`
	DeviceModel string `json:"device_model" diag:"id"`
	TillID      string `json:"till_id" diag:"id"`
	DisplayMode string `json:"display_mode" diag:"enum"`
}

// PluginState is one installed plugin's id/version/checksum/lifecycle
// state — inventory, emitted alongside Environment.
type PluginState struct {
	PluginID string `json:"plugin_id" diag:"id"`
	Version  string `json:"version" diag:"id"`
	Checksum string `json:"checksum" diag:"id"`
	State    string `json:"state" diag:"enum"`
}

// Gap is the capacity marker (ADR-0092 §2): emitted exactly once per drop
// episode when the ring or the on-disk queue hit their cap, carrying how
// many events were dropped (oldest first) — never silently thinned.
type Gap struct {
	DroppedCount int `json:"dropped_count" diag:"count"`
}

func (PluginAsk) eventType() string       { return "plugin_ask" }
func (TaxProvenance) eventType() string   { return "tax_provenance" }
func (TableAssignment) eventType() string { return "table_assignment" }
func (OrderStatus) eventType() string     { return "order_status" }
func (Environment) eventType() string     { return "environment" }
func (PluginState) eventType() string     { return "plugin_state" }
func (Gap) eventType() string             { return "diagnostic_gap" }

// allEvents is the registry the static check and the runtime validator
// iterate. A new event class MUST be added here (and to the cloud's
// AllowedEventTypes) or it is refused by the test AND by Emit.
var allEvents = []Event{
	PluginAsk{}, TaxProvenance{}, TableAssignment{}, OrderStatus{},
	Environment{}, PluginState{}, Gap{},
}

// cloudAllowedTypes mirrors ut-cloud/internal/diagnostics.AllowedEventTypes
// — the ingestion side refuses any other discriminator, so an event type
// added here without the cloud half would be dropped as a 400 on upload.
var cloudAllowedTypes = map[string]bool{
	"plugin_ask": true, "tax_provenance": true, "table_assignment": true,
	"order_status": true, "environment": true, "plugin_state": true,
	"diagnostic_gap": true,
}

// enumValues is the closed vocabulary per `diag:"enum"` field, keyed by
// "<type>.<json name>". Both the static check (every enum field must have a
// non-empty entry; every entry must name a real field) and Emit (a value
// outside the set drops the event) read it.
var enumValues = map[string][]string{
	"plugin_ask.outcome":       {OutcomeAnswer, OutcomeNoOpinion, OutcomeError, OutcomeMalformed},
	"tax_provenance.from":      {ProvenanceActiveOverride, ProvenanceCatalogSuggestion, ProvenanceAbsent},
	"tax_provenance.to":        {ProvenanceActiveOverride, ProvenanceCatalogSuggestion, ProvenanceAbsent},
	"table_assignment.action":  {TableActionClaim, TableActionRelease},
	"table_assignment.outcome": {TableOutcomeClaimed, TableOutcomeRefused, TableOutcomeError},
	"table_assignment.via":     {ViaPrimary, ViaLocal},
	// pos.OrderStatus* — restated here (not imported) so this package stays
	// leaf-level; TestOrderStatusEnumMatchesPOS in internal/pages pins them.
	"order_status.status":      {"new", "preparing", "ready", "collected", "cancelled"},
	"order_status.via":         {ViaPrimary, ViaLocal},
	"environment.display_mode": {DisplayModeRegister, DisplayModeBackoffice, DisplayModeSelfOrder},
	"plugin_state.state":       {PluginStateActive, PluginStateInstalled, PluginStateBroken, PluginStateRevoked, PluginStateFailed, PluginStateUnknown},
}

// allowedKinds is the whole vocabulary a `diag` tag may declare.
var allowedKinds = map[string]bool{"id": true, "enum": true, "count": true, "ms": true, "bool": true}

// maxIDLen bounds an opaque id. Real ids here are uuids, slugs, receipt
// numbers, semver strings and sha256 hex — none approach this.
const maxIDLen = 128

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		name = f.Name
	}
	return name
}

func enumKey(eventType string, f reflect.StructField) string {
	return eventType + "." + jsonName(f)
}

// checkEventShape returns every way typ violates the allowlist invariant
// (empty means compliant). Shared by the static test and, defensively, by
// Emit's one-time registry validation so a violating struct can never be
// emitted even if the test is skipped.
func checkEventShape(typ reflect.Type) []string {
	var problems []string
	if typ.Kind() != reflect.Struct {
		return []string{fmt.Sprintf("%s: must be a struct", typ)}
	}
	ev, ok := reflect.Zero(typ).Interface().(Event)
	if !ok {
		return []string{fmt.Sprintf("%s: does not implement Event", typ)}
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		where := typ.Name() + "." + f.Name
		if !f.IsExported() {
			problems = append(problems, where+": unexported field — every field must be a declared, exported wire field")
			continue
		}
		kind, tagged := f.Tag.Lookup("diag")
		if !tagged {
			problems = append(problems, where+": no diag tag — declare id|enum|count|ms|bool")
			continue
		}
		if !allowedKinds[kind] {
			problems = append(problems, fmt.Sprintf("%s: unknown diag kind %q", where, kind))
			continue
		}
		switch kind {
		case "id", "enum":
			if f.Type.Kind() != reflect.String {
				problems = append(problems, fmt.Sprintf("%s: diag %q must be string, is %s", where, kind, f.Type))
			}
			if kind == "enum" && len(enumValues[enumKey(ev.eventType(), f)]) == 0 {
				problems = append(problems, where+": enum field has no registered vocabulary in enumValues")
			}
		case "count", "ms":
			switch f.Type.Kind() {
			case reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint32, reflect.Uint64:
			default:
				problems = append(problems, fmt.Sprintf("%s: diag %q must be an integer type, is %s", where, kind, f.Type))
			}
		case "bool":
			if f.Type.Kind() != reflect.Bool {
				problems = append(problems, fmt.Sprintf("%s: diag \"bool\" must be bool, is %s", where, f.Type))
			}
		}
	}
	return problems
}

// registryChecked memoizes the one-time shape validation of allEvents so
// Emit never pays reflection per call.
var registryChecked = func() map[reflect.Type]bool {
	out := make(map[reflect.Type]bool, len(allEvents))
	for _, ev := range allEvents {
		typ := reflect.TypeOf(ev)
		out[typ] = len(checkEventShape(typ)) == 0 && cloudAllowedTypes[ev.eventType()]
	}
	return out
}()

// idCharset is every character a real opaque id in this codebase actually
// uses: uuids, device ids ("till-<uuid>"), semver plugin versions, sha256
// hex checksums, slugs and receipt numbers. Deliberately NOT the same as
// "not whitespace/control" — that weaker rule let a "key=value" cookie
// pair (e.g. a session cookie) through untouched, since neither '=' nor
// the rest of a typical cookie value is whitespace or a control character
// (found by review, ut-docs#2169). '=' in particular is excluded on
// purpose: it never appears in any id this package actually emits, and it
// is exactly the character that turns an opaque token into a labelled
// pair. Anything outside this set — a space, a sentence, a stray '=', a
// literal control character — is prose or a credential, not an id, and
// the event is dropped.
func isIDChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || r == '+' || r == '@':
		return true
	default:
		return false
	}
}

// validIDValue is the runtime half of the id invariant: opaque ids are
// short single tokens drawn only from idCharset. A cookie pair, a bearer
// header, a sentence, punctuation, or a control character is not an id and
// drops the event. Empty stays valid — several id fields are legitimately
// absent (e.g. PluginAsk.PluginID when no plugin subscribed at all, which
// is itself a diagnostic-worthy state, not an error to hide by dropping
// the whole event) — the same as this check's original behavior for "".
func validIDValue(s string) bool {
	if len(s) > maxIDLen {
		return false
	}
	for _, r := range s {
		if !isIDChar(r) {
			return false
		}
	}
	return true
}

// validate applies the closed-enum and id rules to one event value, naming
// the offending FIELD (never its value) on failure.
func validate(ev Event) error {
	v := reflect.ValueOf(ev)
	typ := v.Type()
	if !registryChecked[typ] {
		return fmt.Errorf("%s is not a registered allowlisted event", typ)
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Tag.Get("diag") {
		case "id":
			if !validIDValue(v.Field(i).String()) {
				return fmt.Errorf("%s.%s is not an opaque id", typ.Name(), f.Name)
			}
		case "enum":
			val := v.Field(i).String()
			ok := false
			for _, allowed := range enumValues[enumKey(ev.eventType(), f)] {
				if val == allowed {
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("%s.%s is outside its closed vocabulary", typ.Name(), f.Name)
			}
		}
	}
	return nil
}

// marshal produces the wire object: the struct's own json fields plus the
// "type" discriminator and an "at" timestamp (RFC3339Nano, UTC). Flat —
// never nested — which is the structural half ut-cloud's ValidateEvents
// re-checks on ingestion.
func marshal(ev Event, at time.Time) ([]byte, error) {
	fields, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(fields, &obj); err != nil {
		return nil, err
	}
	obj["type"] = ev.eventType()
	obj["at"] = at.UTC().Format(time.RFC3339Nano)
	return json.Marshal(obj)
}

// Emit records one allowlisted event into the in-memory ring when a
// diagnostic session is active, and is a cheap no-op (one atomic load)
// otherwise — zero cost on every till that never activated diagnostics.
// It never blocks, never returns an error and never touches disk or
// network: a refused event (enum/id violation) is dropped with a Debug
// line naming the field only.
func Emit(ev Event) {
	if current.Load() == nil {
		return
	}
	if err := validate(ev); err != nil {
		logging.L().Debugf("diagnostics: event refused: %v", err)
		return
	}
	now := time.Now()
	raw, err := marshal(ev, now)
	if err != nil {
		logging.L().Debugf("diagnostics: event not encodable: %v", err)
		return
	}
	ring.add(raw, now)
}

// EnumValues returns the closed vocabulary registered for one enum field
// ("<type>.<json name>"), or nil — so a caller package (e.g. internal/pages)
// can pin the copy kept here against the real constants it mirrors.
func EnumValues(eventType, field string) []string {
	vals := enumValues[eventType+"."+field]
	if vals == nil {
		return nil
	}
	return append([]string(nil), vals...)
}

// EventTypes lists the registered wire discriminators, sorted — for
// diagnostics/inventory display and tests.
func EventTypes() []string {
	out := make([]string, 0, len(allEvents))
	for _, ev := range allEvents {
		out = append(out, ev.eventType())
	}
	sort.Strings(out)
	return out
}
