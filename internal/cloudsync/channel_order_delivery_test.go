package cloudsync

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ADR-0215 (ut-docs#6581): the optional `delivery` object, `note`, per-line
// `modifiers`/`note` and the delivery statuses of a channel_order payload.

func deliveryDirective(t *testing.T) directive {
	d := fixtureDirective(t)
	p := d.Payload
	p["status"] = "new"
	p["delivery"] = map[string]any{
		"service": "delivery", "accept_by": "2031-12-01T12:10:00Z", "scheduled_start_at": "2031-12-01T12:30:00Z",
		"confirm_at": "2031-12-01T12:20:00Z", "prep_minutes": 15.0, "courier_ref": "c-9", "pickup_code": "A7", "tip_minor": 150.0,
		"fees": []any{map[string]any{"kind": "delivery_fee", "amount_minor": 199.0}},
	}
	p["note"] = "No nuts please\nsevere allergy"
	l := p["lines"].([]any)[0].(map[string]any)
	l["note"] = "extra napkins"
	l["modifiers"] = []any{map[string]any{"external_id": "m1", "name": "Large", "qty": 1.0, "price_delta_minor": 100.0}}
	return d
}

func TestDecodeChannelOrder_DeliveryFields(t *testing.T) {
	in, bad := decodeChannelOrder(deliveryDirective(t))
	if bad != "" {
		t.Fatalf("refused: %s", bad)
	}
	d := in.Delivery
	if d == nil || d.Service != "delivery" || d.AcceptBy != "2031-12-01T12:10:00Z" || d.ScheduledStartAt != "2031-12-01T12:30:00Z" ||
		d.ConfirmAt != "2031-12-01T12:20:00Z" || d.PrepMinutes != 15 || d.CourierRef != "c-9" || d.PickupCode != "A7" ||
		d.Tip != money.FromMinor(150) || len(d.Fees) != 1 || d.Fees[0].Amount != money.FromMinor(199) {
		t.Fatalf("delivery = %+v", d)
	}
	if in.Note != "No nuts please\nsevere allergy" || in.Lines[0].Note != "extra napkins" ||
		len(in.Lines[0].Modifiers) != 1 || in.Lines[0].Modifiers[0].PriceDelta != money.FromMinor(100) {
		t.Fatalf("notes/modifiers = %q %+v", in.Note, in.Lines[0])
	}
}

func TestDecodeChannelOrder_DeliveryStatuses(t *testing.T) {
	for _, st := range []string{"new", "accepted", "rejected", "preparing", "ready", "collected", "cancelled"} {
		d := deliveryDirective(t)
		d.Payload["status"] = st
		if _, bad := decodeChannelOrder(d); bad != "" {
			t.Errorf("%s refused: %s", st, bad)
		}
	}
	for _, st := range []string{"picked", "packed", "shipped"} {
		d := deliveryDirective(t)
		d.Payload["status"] = st
		if _, bad := decodeChannelOrder(d); bad == "" {
			t.Errorf("%s must be refused on a delivery order", st)
		}
	}
	for _, st := range []string{"rejected", "preparing", "ready", "collected"} {
		d := fixtureDirective(t)
		d.Payload["status"] = st
		if _, bad := decodeChannelOrder(d); bad == "" {
			t.Errorf("%s must be refused without a delivery object", st)
		}
	}
}

func TestDecodeChannelOrder_DeliveryRefusals(t *testing.T) {
	cases := map[string]func(p map[string]any){
		"unknown delivery key (no customer data rides in)": func(p map[string]any) { p["delivery"].(map[string]any)["customer_phone"] = "1" },
		"customer name in delivery":                        func(p map[string]any) { p["delivery"].(map[string]any)["address"] = "1 High St" },
		"bad service":                                      func(p map[string]any) { p["delivery"].(map[string]any)["service"] = "drone" },
		"bad accept_by":                                    func(p map[string]any) { p["delivery"].(map[string]any)["accept_by"] = "tomorrow" },
		"negative tip":                                     func(p map[string]any) { p["delivery"].(map[string]any)["tip_minor"] = -1.0 },
		"fractional prep":                                  func(p map[string]any) { p["delivery"].(map[string]any)["prep_minutes"] = 1.5 },
		"huge prep":                                        func(p map[string]any) { p["delivery"].(map[string]any)["prep_minutes"] = 100000.0 },
		"fee unknown key": func(p map[string]any) {
			p["delivery"].(map[string]any)["fees"].([]any)[0].(map[string]any)["payer"] = "x"
		},
		"fee negative": func(p map[string]any) {
			p["delivery"].(map[string]any)["fees"].([]any)[0].(map[string]any)["amount_minor"] = -5.0
		},
		"note too long":      func(p map[string]any) { p["note"] = strings.Repeat("a", 501) },
		"note control char":  func(p map[string]any) { p["note"] = "a\x00b" },
		"note bidi override": func(p map[string]any) { p["note"] = "a‮b" },
		"note not a string":  func(p map[string]any) { p["note"] = 5.0 },
		"line note too long": func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["note"] = strings.Repeat("a", 501) },
		"modifier unknown key": func(p map[string]any) {
			p["lines"].([]any)[0].(map[string]any)["modifiers"].([]any)[0].(map[string]any)["phone"] = "1"
		},
		"modifier no name": func(p map[string]any) {
			p["lines"].([]any)[0].(map[string]any)["modifiers"].([]any)[0].(map[string]any)["name"] = ""
		},
		"modifier qty zero": func(p map[string]any) {
			p["lines"].([]any)[0].(map[string]any)["modifiers"].([]any)[0].(map[string]any)["qty"] = 0.0
		},
		"delivery not an object": func(p map[string]any) { p["delivery"] = "yes" },
	}
	for name, mutate := range cases {
		d := deliveryDirective(t)
		mutate(d.Payload)
		in, bad := decodeChannelOrder(d)
		if bad == "" {
			t.Errorf("%s: accepted %+v", name, in.Delivery)
			continue
		}
		if strings.Contains(bad, "customer_phone") && !strings.Contains(name, "unknown") {
			t.Errorf("%s: refusal echoes a value: %s", name, bad)
		}
	}
}

func TestDecodeChannelOrder_NoteOnlyInsideOrderAndLineScope(t *testing.T) {
	d := fixtureDirective(t)
	d.Payload["note"] = "gift wrap" // a webshop order without delivery may not carry a note
	if _, bad := decodeChannelOrder(d); bad == "" {
		t.Fatal("a note without a delivery object must be refused")
	}
}
