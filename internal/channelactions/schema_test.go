package channelactions

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
)

// T-schema-drift: the vendored schema equals ut-docs main's. Compares against
// CHANNEL_ORDER_ACTION_SCHEMA or a sibling ut-docs checkout when one exists;
// CI has no ut-docs checkout, so there TestWire_* are what run. Refresh with
// `cp ../ut-docs/reference/contracts/channel-order-action-v1.json internal/channelactions/testdata/`.
func TestVendoredSchemaIsCurrent(t *testing.T) {
	path := os.Getenv("CHANNEL_ORDER_ACTION_SCHEMA")
	if path == "" {
		for _, c := range []string{"../../../ut-docs/reference/contracts/channel-order-action-v1.json", "../../../../ut-docs/reference/contracts/channel-order-action-v1.json", "../../../../../ut-docs/reference/contracts/channel-order-action-v1.json"} {
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}
	if path == "" {
		t.Skip("no ut-docs checkout: set CHANNEL_ORDER_ACTION_SCHEMA to check the vendored copy for drift")
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("testdata/channel-order-action-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("testdata/channel-order-action-v1.json differs from %s: copy the ut-docs file over it", path)
	}
}

// T-wire: every marshalled action kind satisfies the vendored schema. The repo
// has no JSON Schema library, so this applies the schema's own properties,
// required, enum, pattern, length and if/then rules directly.
func TestWire_EveryActionKindSatisfiesTheVendoredSchema(t *testing.T) {
	raw, err := os.ReadFile("testdata/channel-order-action-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	defs := schema["$defs"].(map[string]any)
	actionDef := defs["action"].(map[string]any)
	props := actionDef["properties"].(map[string]any)

	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	base := data.ChannelOrderAction{ActionID: "0f9d4a52-6c1e-4b7a-9d3e-2a5b8c7e1f40", ConnectionID: connID, ExternalOrderID: "o-9001",
		BaseRevision: 3, CreatedAt: "2026-10-23T09:12:44Z", StaffID: "u-7f3a"}
	kinds := map[string]data.ChannelOrderAction{}
	for _, a := range []string{"accept", "pick", "pack"} {
		x := base
		x.Action = a
		kinds[a] = x
	}
	ship := base
	ship.Action, ship.Shipment = "ship", &data.ChannelShipment{Carrier: "Royal Mail", TrackingNumber: "AB123456789GB", TrackingURL: "https://tracking.example/AB123456789GB"}
	kinds["ship"] = ship
	shipMin := base
	shipMin.Action, shipMin.Shipment = "ship", &data.ChannelShipment{Carrier: "Royal Mail"}
	kinds["ship minimal"] = shipMin
	cancel := base
	cancel.Action, cancel.Cancel = "cancel", &data.ChannelCancel{Reason: "other", Restock: false}
	kinds["cancel restock false"] = cancel
	noStaff := base
	noStaff.Action, noStaff.StaffID = "accept", ""
	kinds["empty staff"] = noStaff
	stale := base
	stale.Action, stale.CreatedAt = "accept", "2026-01-01T00:00:00Z"
	kinds["stale"] = stale

	for name, row := range kinds {
		body, err := encodeBody("store-ext-id", []Action{ToWire(row, now)})
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		if doc["store_id"] != "store-ext-id" || len(doc) != 2 {
			t.Fatalf("%s: envelope = %v", name, doc)
		}
		a := doc["actions"].([]any)[0].(map[string]any)
		for k := range a {
			if _, ok := props[k]; !ok {
				t.Errorf("%s: key %q is not in the schema", name, k)
			}
		}
		for _, k := range actionDef["required"].([]any) {
			if _, ok := a[k.(string)]; !ok {
				t.Errorf("%s: required key %q missing", name, k)
			}
		}
		checkPattern(t, name, "action_id", a["action_id"], defs["uuid"].(map[string]any)["pattern"])
		checkPattern(t, name, "connection_id", a["connection_id"], defs["uuid"].(map[string]any)["pattern"])
		checkPattern(t, name, "created_at", a["created_at"], props["created_at"].(map[string]any)["pattern"])
		if l := utf8.RuneCountInString(a["staff_id"].(string)); l > 128 {
			t.Errorf("%s: staff_id too long", name)
		}
		if a["base_revision"].(float64) < 1 {
			t.Errorf("%s: base_revision < 1", name)
		}
		if !inEnum(props["action"], a["action"]) {
			t.Errorf("%s: action %v not in enum", name, a["action"])
		}
		if v, has := a["stale"]; has && v != true {
			t.Errorf("%s: stale must be present only when true", name)
		}
		if (name == "stale") != (a["stale"] == true) {
			t.Errorf("%s: stale = %v", name, a["stale"])
		}
		_, hasShip := a["shipment"]
		_, hasCancel := a["cancel"]
		switch a["action"] {
		case "ship":
			if !hasShip || hasCancel {
				t.Errorf("%s: ship needs shipment only", name)
			}
		case "cancel":
			if !hasCancel || hasShip {
				t.Errorf("%s: cancel needs cancel only", name)
			}
		default:
			if hasShip || hasCancel {
				t.Errorf("%s: %v takes neither", name, a["action"])
			}
		}
		if hasShip {
			sp := props["shipment"].(map[string]any)["properties"].(map[string]any)
			s := a["shipment"].(map[string]any)
			for k := range s {
				if _, ok := sp[k]; !ok {
					t.Errorf("%s: shipment key %q not in schema", name, k)
				}
			}
			if _, ok := s["carrier"]; !ok {
				t.Errorf("%s: carrier missing", name)
			}
			if u, ok := s["tracking_url"]; ok {
				if len(u.(string)) > 512 {
					t.Errorf("%s: url too long", name)
				}
			}
		}
		if hasCancel {
			cp := props["cancel"].(map[string]any)["properties"].(map[string]any)
			c := a["cancel"].(map[string]any)
			if len(c) != 2 {
				t.Errorf("%s: cancel = %v (restock:false must still be sent)", name, c)
			}
			for k := range c {
				if _, ok := cp[k]; !ok {
					t.Errorf("%s: cancel key %q not in schema", name, k)
				}
			}
			if !inEnum(cp["reason"], c["reason"]) {
				t.Errorf("%s: reason not in enum", name)
			}
			if _, ok := c["restock"].(bool); !ok {
				t.Errorf("%s: restock not a boolean", name)
			}
		}
	}
}

// The schema's reasons are the validator's reasons.
func TestWire_CancelReasonsMatchTheSchemaEnum(t *testing.T) {
	raw, _ := os.ReadFile("testdata/channel-order-action-v1.json")
	var schema struct {
		Defs struct {
			Action struct {
				Properties struct {
					Cancel struct {
						Properties struct {
							Reason struct {
								Enum []string `json:"enum"`
							} `json:"reason"`
						} `json:"properties"`
					} `json:"cancel"`
				} `json:"properties"`
			} `json:"action"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	enum := schema.Defs.Action.Properties.Cancel.Properties.Reason.Enum
	if len(enum) != len(CancelReasons) {
		t.Fatalf("schema %v, code %v", enum, CancelReasons)
	}
	for i := range enum {
		if enum[i] != CancelReasons[i] {
			t.Fatalf("schema %v, code %v", enum, CancelReasons)
		}
	}
}

func checkPattern(t *testing.T, name, field string, v, pat any) {
	t.Helper()
	s, _ := v.(string)
	if !regexp.MustCompile(pat.(string)).MatchString(s) {
		t.Errorf("%s: %s %q does not match the schema pattern", name, field, s)
	}
}

func inEnum(def any, v any) bool {
	for _, e := range def.(map[string]any)["enum"].([]any) {
		if e == v {
			return true
		}
	}
	return false
}
