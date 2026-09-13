package diagnostics

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestAllowlistedEvents_FieldTypesAreClosed is ADR-0092 §2's package-level
// static check: every field of every allowlisted event struct must declare,
// via its `diag` struct tag, which of the small allowed kinds it is (opaque
// id, closed enum, bounded count/duration, bool), its Go type must match
// that kind, and an enum field must have a registered, non-empty closed
// value set. A `string` Go type is used for BOTH ids/enums and free text,
// so "no string fields" would reject legitimate ids while "strings are
// fine" would wave through prose — hence the per-field declaration: a
// future field with no `diag` tag, an unknown kind, or an enum with no
// registered vocabulary fails here, not in review.
func TestAllowlistedEvents_FieldTypesAreClosed(t *testing.T) {
	if len(allEvents) == 0 {
		t.Fatal("allEvents registry is empty — nothing is being checked")
	}
	for _, ev := range allEvents {
		typ := reflect.TypeOf(ev)
		if problems := checkEventShape(typ); len(problems) != 0 {
			t.Errorf("%s violates the allowlist invariant:\n  %s", typ.Name(), strings.Join(problems, "\n  "))
		}
		if !cloudAllowedTypes[ev.eventType()] {
			t.Errorf("%s emits type %q, which ut-cloud's AllowedEventTypes does not accept", typ.Name(), ev.eventType())
		}
	}
	// Every registered enum vocabulary must belong to a real field — an
	// orphan entry would let a renamed field silently lose its check.
	for key := range enumValues {
		found := false
		for _, ev := range allEvents {
			typ := reflect.TypeOf(ev)
			for i := 0; i < typ.NumField(); i++ {
				if enumKey(ev.eventType(), typ.Field(i)) == key {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("enumValues[%q] names no field on any registered event", key)
		}
	}
}

// Violating shapes the checker MUST reject — proves the static check above
// is not vacuous. Each is the exact class of mistake a future contributor
// could make: a free-text field with no declaration, a made-up kind, an
// enum with no vocabulary, raw bytes, and a nested structure.
type badUntagged struct {
	Note string `json:"note"`
}
type badTextKind struct {
	Label string `json:"label" diag:"text"`
}
type badEnumNoVocab struct {
	Mode string `json:"mode" diag:"enum"`
}
type badRawBytes struct {
	Response []byte `json:"response" diag:"id"`
}
type badNested struct {
	Payload map[string]any `json:"payload" diag:"id"`
}
type badKindTypeMismatch struct {
	Count string `json:"count" diag:"count"`
}
type badUnexported struct {
	secret string //nolint:unused // the whole point: the checker must see it
}

func (badUntagged) eventType() string         { return "plugin_ask" }
func (badTextKind) eventType() string         { return "plugin_ask" }
func (badEnumNoVocab) eventType() string      { return "plugin_ask" }
func (badRawBytes) eventType() string         { return "plugin_ask" }
func (badNested) eventType() string           { return "plugin_ask" }
func (badKindTypeMismatch) eventType() string { return "plugin_ask" }
func (badUnexported) eventType() string       { return "plugin_ask" }

func TestCheckEventShape_RejectsViolatingStructs(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		want string
	}{
		{"untagged free-text string", badUntagged{}, "no diag tag"},
		{"unknown kind", badTextKind{}, "unknown diag kind"},
		{"enum without vocabulary", badEnumNoVocab{}, "no registered vocabulary"},
		{"raw bytes", badRawBytes{}, "must be string"},
		{"nested map", badNested{}, "must be string"},
		{"count declared on a string", badKindTypeMismatch{}, "must be an integer"},
		{"unexported field", badUnexported{}, "unexported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := checkEventShape(reflect.TypeOf(tc.ev))
			if len(problems) == 0 {
				t.Fatalf("checkEventShape accepted %T — the static check is vacuous", tc.ev)
			}
			if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Fatalf("problems for %T = %q, want one mentioning %q", tc.ev, problems, tc.want)
			}
		})
	}
}

// withActiveSession makes Emit live for one test, against an isolated ring.
func withActiveSession(t *testing.T) *memKV {
	t.Helper()
	kv := newMemKV()
	resetForTest(t)
	if err := Activate(t.Context(), kv, "11111111-2222-3333-4444-555555555555", time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return kv
}

// secrets is the representative set every adversarial test below has "in
// scope": an operator PIN, a card token, a session cookie value and a
// bearer Authorization header value. None may ever appear in an emitted
// event's marshaled bytes.
var secrets = map[string]string{
	"pin":            "4321",
	"card_token":     "tok_4242424242424242",
	"session_cookie": "ut_session=9f1c2b7e8a6d4c3b2a1f0e9d8c7b6a5f",
	"authorization":  "Bearer eyJhbGciOiJIUzI1NiJ9.c2VjcmV0.c2ln",
}

func assertNoSecret(t *testing.T, marshaled []byte) {
	t.Helper()
	for name, s := range secrets {
		if strings.Contains(string(marshaled), s) {
			t.Fatalf("emitted event leaks the %s secret: %s", name, marshaled)
		}
	}
}

// drainRing returns every event currently buffered, marshaled, and empties
// the ring.
func drainRing(t *testing.T) [][]byte {
	t.Helper()
	ring.mu.Lock()
	defer ring.mu.Unlock()
	out := make([][]byte, 0, len(ring.events))
	for _, e := range ring.events {
		out = append(out, e.raw)
	}
	ring.events = nil
	return out
}

// For EVERY allowlisted event type: the call is constructed the way its
// real call site constructs it, with every secret above in scope — the raw
// ask payload / plugin response / form body / request header that carried
// the secret is right there in the test — and the emitted, marshaled event
// must not contain any of them. Additionally each enum and id field is
// offered every secret directly, and Emit must REFUSE the event (closed
// enum / id charset+length), so the secret cannot ride even a misuse of
// the typed API.
func TestAdversarial_NoSecretReachesAnyEventType(t *testing.T) {
	withActiveSession(t)

	// The realistic scope: things a call site has in hand next to the
	// values it is allowed to emit.
	askPayload := `{"item_id":"i-1","card_token":"` + secrets["card_token"] + `","pin":"` + secrets["pin"] + `"}`
	pluginResponse := `{"rate_bp":700,"debug":"` + secrets["authorization"] + `"}`
	formBody := "status=ready&pin=" + secrets["pin"]
	cookieHeader := secrets["session_cookie"]
	_ = askPayload
	_ = pluginResponse
	_ = formBody
	_ = cookieHeader

	realistic := []Event{
		PluginAsk{Event: "tax.rate.ask", PluginID: "ut-plugin-tax-de", PluginVersion: "1.2.0", CacheHit: false, Generation: 3, DurationMS: 12, CorrelationID: "c-1", Outcome: OutcomeAnswer},
		TaxProvenance{TaxCodeID: "tc-7", From: ProvenanceCatalogSuggestion, To: ProvenanceActiveOverride},
		TableAssignment{TableID: "t-4", Action: TableActionClaim, Outcome: TableOutcomeClaimed, Via: ViaLocal},
		OrderStatus{OrderID: "R-000123", Status: "ready", Applied: true, Via: ViaPrimary},
		Environment{AppVersion: "0.8.6", OS: "android", Arch: "arm64", DeviceModel: "SM-X200", TillID: "till-1", DisplayMode: DisplayModeRegister},
		PluginState{PluginID: "ut-plugin-tax-de", Version: "1.2.0", Checksum: "sha256:ab12", State: PluginStateActive},
		Gap{DroppedCount: 17},
	}
	if len(realistic) != len(allEvents) {
		t.Fatalf("this test covers %d event types but %d are registered — add the new one here", len(realistic), len(allEvents))
	}
	for _, ev := range realistic {
		Emit(ev)
		got := drainRing(t)
		if len(got) != 1 {
			t.Fatalf("%T: emitted %d events, want 1", ev, len(got))
		}
		assertNoSecret(t, got[0])
		var obj map[string]any
		if err := json.Unmarshal(got[0], &obj); err != nil {
			t.Fatalf("%T: not a JSON object: %v", ev, err)
		}
		if obj["type"] != ev.eventType() {
			t.Fatalf("%T: type = %v, want %q", ev, obj["type"], ev.eventType())
		}
		if _, ok := obj["at"].(string); !ok {
			t.Fatalf("%T: missing at timestamp: %s", ev, got[0])
		}
	}

	// Misuse: a secret pushed straight into every string field of every
	// type. Enum fields refuse anything outside their vocabulary; id fields
	// refuse the whitespace/length/charset these secrets carry (a cookie
	// pair, a bearer header). A bare 4-digit PIN DOES look like an opaque
	// id — that case is covered by construction (no call site hands an id
	// field anything but a real id) and is deliberately not claimed here.
	for _, ev := range allEvents {
		typ := reflect.TypeOf(ev)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Type.Kind() != reflect.String {
				continue
			}
			for name, s := range secrets {
				if name == "pin" && f.Tag.Get("diag") == "id" {
					continue
				}
				v := reflect.New(typ).Elem()
				v.Field(i).SetString(s)
				Emit(v.Interface().(Event))
				for _, raw := range drainRing(t) {
					assertNoSecret(t, raw)
				}
			}
		}
	}
}

// Emit is a no-op when no session is active — nothing buffered, and the
// early return costs at most the interface boxing at the call site.
func TestEmit_NoOpWhenInactive(t *testing.T) {
	resetForTest(t)
	Emit(Gap{DroppedCount: 1})
	if got := drainRing(t); len(got) != 0 {
		t.Fatalf("inactive Emit buffered %d events", len(got))
	}
	allocs := testing.AllocsPerRun(100, func() { Emit(Gap{DroppedCount: 1}) })
	if allocs > 1 {
		t.Fatalf("inactive Emit allocates %.1f/op, want <= 1 (the interface box)", allocs)
	}
}

// An enum value outside the registered vocabulary is refused outright —
// the event is dropped, never emitted with the bad value.
func TestEmit_RefusesUnknownEnumValue(t *testing.T) {
	withActiveSession(t)
	Emit(TaxProvenance{TaxCodeID: "tc-1", From: "operator typed this", To: ProvenanceAbsent})
	if got := drainRing(t); len(got) != 0 {
		t.Fatalf("Emit accepted an unregistered enum value: %s", got[0])
	}
}

// TestEmit_RefusesIDFieldWithProseOrCookieValue proves the id charset
// check (validIDValue) actually fires on its own — not merely that some
// OTHER field on the same struct happens to be invalid too. Review
// (ut-docs#2169) found that TestAdversarial_NoSecretReachesAnyEventType's
// field-by-field misuse loop never exercised this in isolation: it leaves
// every other field zero-valued, so an enum field failing first always
// masked whatever the id check would have done — a scratch PoC with
// `PluginID: "ut_session=9f1c2b7e8a6d4c3b2a1f0e9d8c7b6a5f"` and every OTHER
// field left realistic/valid reached the wire in full, on the
// pre-review charset (whitespace/control-only). Every case here keeps
// every other field realistic and valid, changing ONLY the id field under
// test, so a failure can only be attributed to that one field.
func TestEmit_RefusesIDFieldWithProseOrCookieValue(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
	}{
		{"cookie pair", PluginAsk{Event: "tax.rate.ask", PluginID: "ut_session=9f1c2b7e8a6d4c3b2a1f0e9d8c7b6a5f", PluginVersion: "1.2.0", Generation: 3, DurationMS: 12, CorrelationID: "c-1", Outcome: OutcomeAnswer}},
		{"bearer header", PluginAsk{Event: "tax.rate.ask", PluginID: "ut-plugin-tax-de", PluginVersion: "Bearer eyJhbGciOiJIUzI1NiJ9.c2VjcmV0.c2ln", Generation: 3, DurationMS: 12, CorrelationID: "c-1", Outcome: OutcomeAnswer}},
		{"prose sentence", TaxProvenance{TaxCodeID: "the takeaway rate for this item", From: ProvenanceCatalogSuggestion, To: ProvenanceActiveOverride}},
		{"operator label", TableAssignment{TableID: "Window Seat #4", Action: TableActionClaim, Outcome: TableOutcomeClaimed, Via: ViaLocal}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withActiveSession(t)
			Emit(c.ev)
			if got := drainRing(t); len(got) != 0 {
				t.Fatalf("Emit accepted a prose/cookie-shaped id value: %s", got[0])
			}
		})
	}
}
