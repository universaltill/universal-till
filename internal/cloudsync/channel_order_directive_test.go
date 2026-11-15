package cloudsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// channel_order / channel_order_update (ADR-0169 §9, ut-docs#4768; payload
// shape reference/contracts/channel-order-v1.json). The fixture is a copy of
// the contract's example; scripts/ci diffs it against ut-docs main.

func fixturePayload(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/channel_order_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixtureDirective(t *testing.T) directive {
	return directive{ID: "dir-co-1", Type: "channel_order", Payload: fixturePayload(t)}
}

func TestChannelOrderIsMainTillOnly(t *testing.T) {
	for _, typ := range []string{"channel_order", "channel_order_update"} {
		if !mainTillOnlyTypes[typ] {
			t.Errorf("%s must stay pending for the main till on a satellite", typ)
		}
	}
}

func TestDecodeChannelOrder_Fixture(t *testing.T) {
	in, bad := decodeChannelOrder(fixtureDirective(t))
	if bad != "" {
		t.Fatalf("refused: %s", bad)
	}
	if in.ConnectionID != "0b3b9c34-5c1e-4a3e-8f55-3f7f3a9a7c02" || in.ExternalOrderID != "o-9001" || in.Revision != 1 ||
		in.OrderNumber != "#1001" || in.Currency != "GBP" || in.Status != "new" || in.PaymentState != "paid" ||
		in.Total != money.FromMinor(7599) || in.Shipping != money.FromMinor(399) || in.CustomerCountry != "GB" ||
		!in.ShippingTaxIncluded || in.ShippingTaxBP != 2000 {
		t.Fatalf("order = %+v", in)
	}
	if in.DirectiveID != "dir-co-1" {
		t.Fatalf("identity must be the envelope id, got %q", in.DirectiveID)
	}
	if len(in.Lines) != 2 || in.Lines[0] != (data.ChannelOrderLineInput{LineID: "l-1", CloudItemID: "itm1", SKU: "LS-M-BLU",
		Name: "Linen shirt, blue, M", Qty: 2, UnitPrice: money.FromMinor(3500), TaxBP: 2000, TaxIncluded: true}) {
		t.Fatalf("lines = %+v", in.Lines)
	}
	if in.Lines[1].SKU != "" || in.Lines[1].CloudItemID != "" {
		t.Fatalf("unlinked line = %+v", in.Lines[1])
	}
}

func TestDecodeChannelOrder_Refusals(t *testing.T) {
	cases := map[string]func(p map[string]any){
		"customer_email":          func(p map[string]any) { p["customer_email"] = "a@b.c" },
		"customer name":           func(p map[string]any) { p["customer"] = map[string]any{"name": "X"} },
		"unknown field":           func(p map[string]any) { p["surprise"] = 1.0 },
		"unknown line field":      func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["phone"] = "1" },
		"missing revision":        func(p map[string]any) { delete(p, "revision") },
		"zero revision":           func(p map[string]any) { p["revision"] = 0.0 },
		"fractional revision":     func(p map[string]any) { p["revision"] = 1.5 },
		"missing order id":        func(p map[string]any) { delete(p, "external_order_id") },
		"missing connection":      func(p map[string]any) { delete(p, "connection_id") },
		"bad status":              func(p map[string]any) { p["status"] = "weird" },
		"bad currency":            func(p map[string]any) { p["currency"] = "gbp" },
		"bad date":                func(p map[string]any) { p["placed_at"] = "yesterday" },
		"no lines":                func(p map[string]any) { p["lines"] = []any{} },
		"negative money":          func(p map[string]any) { p["shipping_minor"] = -1.0 },
		"fractional money":        func(p map[string]any) { p["total_minor"] = 10.5 },
		"string money":            func(p map[string]any) { p["total_minor"] = "7599" },
		"zero qty":                func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["qty"] = 0.0 },
		"refunded over qty":       func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["refunded_quantity"] = 3.0 },
		"refund over total":       func(p map[string]any) { p["refund_total_minor"] = 99999.0 },
		"missing line name":       func(p map[string]any) { delete(p["lines"].([]any)[0].(map[string]any), "name") },
		"bad country":             func(p map[string]any) { p["customer_country"] = "Britain" },
		"payload directive_id":    func(p map[string]any) { p["directive_id"] = "dir-co-1" },
		"missing schema_version":  func(p map[string]any) { delete(p, "schema_version") },
		"unknown schema_version":  func(p map[string]any) { p["schema_version"] = 2.0 },
		"offset timestamp":        func(p map[string]any) { p["placed_at"] = "2026-10-22T10:12:00+01:00" },
		"tax above 100%":          func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["tax_bp"] = 10001.0 },
		"shipping tax above 100%": func(p map[string]any) { p["shipping_tax_bp"] = 10001.0 },
		"restocked over refunded": func(p map[string]any) {
			l := p["lines"].([]any)[0].(map[string]any)
			l["refunded_quantity"], l["restocked_quantity"] = 1.0, 2.0
		},
		"old restock flag":       func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["restock"] = true },
		"empty cloud_variant_id": func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["cloud_variant_id"] = "" },
		"empty cloud_item_id":    func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["cloud_item_id"] = "" },
		"discount over line":     func(p map[string]any) { p["lines"].([]any)[0].(map[string]any)["discount_line_minor"] = 7001.0 },
		"501 lines": func(p map[string]any) {
			l := p["lines"].([]any)[0]
			ls := make([]any, 501)
			for i := range ls {
				ls[i] = l
			}
			p["lines"] = ls
		},
	}
	for name, mut := range cases {
		d := fixtureDirective(t)
		mut(d.Payload)
		if _, bad := decodeChannelOrder(d); bad == "" {
			t.Errorf("%s: want a refusal", name)
		} else if strings.Contains(bad, "a@b.c") {
			t.Errorf("%s: the refusal echoes the value: %q", name, bad)
		}
	}
}

func TestDecodeChannelOrder_TotalMismatchIsFlaggedNotRefused(t *testing.T) {
	d := fixtureDirective(t)
	d.Payload["total_minor"] = 7000.0
	in, bad := decodeChannelOrder(d)
	if bad != "" {
		t.Fatalf("a total that disagrees with the lines must still be recorded (stock is real): %s", bad)
	}
	if !in.TotalMismatch {
		t.Fatal("TotalMismatch not set")
	}
	ok, _ := decodeChannelOrder(fixtureDirective(t))
	if ok.TotalMismatch {
		t.Fatal("the fixture's total satisfies the contract formula")
	}
}

func TestApplyChannelOrder_RoutesBothTypesToTheHook(t *testing.T) {
	var got []data.ChannelOrderInput
	hooks := Hooks{ApplyChannelOrder: func(_ context.Context, in ChannelOrderDirective) (string, error) {
		got = append(got, in.ChannelOrderInput)
		return "recorded", nil
	}}
	for _, typ := range []string{"channel_order", "channel_order_update"} {
		d := fixtureDirective(t)
		d.Type = typ
		if status, msg := apply(context.Background(), d, hooks); status != "applied" || msg != "recorded" {
			t.Errorf("%s: %s %q", typ, status, msg)
		}
	}
	if len(got) != 2 {
		t.Fatalf("hook calls = %d", len(got))
	}
	bad := fixtureDirective(t)
	bad.Payload["customer_email"] = "x@y.z"
	if status, _ := apply(context.Background(), bad, hooks); status != "failed" || len(got) != 2 {
		t.Fatal("a payload with customer data must fail without reaching the hook")
	}
	un := fixtureDirective(t)
	un.Payload["schema_version"] = 2.0
	if status, msg := apply(context.Background(), un, hooks); status != "failed" || !strings.Contains(msg, "schema_version") || len(got) != 2 {
		t.Fatalf("unknown schema_version: %s %q", status, msg)
	}
	if status, msg := apply(context.Background(), fixtureDirective(t), Hooks{}); status != "failed" || !strings.Contains(msg, "not supported") {
		t.Fatalf("no hook: %s %q", status, msg)
	}
}

// The decoder's field sets must equal the contract schema's, and the fixture
// must carry every required field. Reads the vendored copy of ut-docs
// reference/contracts/channel-order-v1.json (testdata/channel-order-v1.json)
// so it always runs in CI; TestChannelOrderVendoredSchemaIsCurrent keeps the
// copy honest.
func TestChannelOrderDecoderMatchesContractSchema(t *testing.T) {
	path := "testdata/channel-order-v1.json"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	lines := schema.Properties["lines"]["items"].(map[string]any)
	lineProps := lines["properties"].(map[string]any)
	if len(schema.Properties) != len(channelOrderFields) {
		t.Errorf("schema has %d top-level fields, decoder allows %d", len(schema.Properties), len(channelOrderFields))
	}
	for k := range schema.Properties {
		if !channelOrderFields[k] {
			t.Errorf("schema field %q is refused by the decoder", k)
		}
	}
	for k := range channelOrderFields {
		if _, ok := schema.Properties[k]; !ok {
			t.Errorf("decoder allows %q which the schema does not define", k)
		}
	}
	if len(lineProps) != len(channelLineFields) {
		t.Errorf("schema has %d line fields, decoder allows %d", len(lineProps), len(channelLineFields))
	}
	for k := range lineProps {
		if !channelLineFields[k] {
			t.Errorf("schema line field %q is refused by the decoder", k)
		}
	}
	p := fixturePayload(t)
	for _, k := range schema.Required {
		if _, ok := p[k]; !ok {
			t.Errorf("fixture lacks required field %q", k)
		}
	}
	for _, k := range lines["required"].([]any) {
		for _, l := range p["lines"].([]any) {
			if _, ok := l.(map[string]any)[k.(string)]; !ok {
				t.Errorf("fixture line lacks required field %q", k)
			}
		}
	}
}

// The vendored schema must equal ut-docs main's. Compares against
// CHANNEL_ORDER_SCHEMA or a sibling ut-docs checkout when one exists (a local
// pipeline session always has it); CI has no ut-docs checkout, so there the
// decoder test above is what runs. Refresh the copy with
// `cp ../ut-docs/reference/contracts/channel-order-v1.json internal/cloudsync/testdata/`.
func TestChannelOrderVendoredSchemaIsCurrent(t *testing.T) {
	path := os.Getenv("CHANNEL_ORDER_SCHEMA")
	if path == "" {
		for _, c := range []string{"../../../ut-docs/reference/contracts/channel-order-v1.json", "../../../../ut-docs/reference/contracts/channel-order-v1.json", "../../../../../ut-docs/reference/contracts/channel-order-v1.json"} {
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}
	if path == "" {
		t.Skip("no ut-docs checkout: set CHANNEL_ORDER_SCHEMA to check the vendored copy for drift")
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("testdata/channel-order-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("testdata/channel-order-v1.json differs from %s: copy the ut-docs file over it", path)
	}
}

// Review M3: a repeated line external_id is refused (it would collapse two
// lines onto one stored row).
func TestDecodeChannelOrder_DuplicateLineIDRefused(t *testing.T) {
	d := fixtureDirective(t)
	lines := d.Payload["lines"].([]any)
	lines[1].(map[string]any)["external_id"] = "l-1"
	if _, msg := decodeChannelOrder(d); !strings.Contains(msg, "duplicate") {
		t.Fatalf("msg = %q", msg)
	}
}

// Review L: a channel order changes stock, which the catalog snapshot reports.
func TestChannelOrderTriggersCatalogSnapshot(t *testing.T) {
	for _, ty := range []string{"channel_order", "channel_order_update"} {
		if !catalogTypes[ty] {
			t.Errorf("%s is not a catalogTypes trigger", ty)
		}
	}
}

// Review H1: a transient failure (db busy, no stock location, cancelled
// context) leaves the directive pending for the next delivery; only decode
// refusals are final.
func TestTick_ChannelOrderTransientFailureIsRetriedNotStoredFailed(t *testing.T) {
	calls := 0
	hooks := Hooks{ApplyChannelOrder: func(context.Context, ChannelOrderDirective) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("no stock location configured")
		}
		return "recorded", nil
	}}
	dir := map[string]any{"id": "dir-co-1", "type": "channel_order", "payload": fixturePayload(t)}
	cloud := &guestCloud{batches: [][]map[string]any{{dir}, {dir}}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "co_transient.db").DB
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatal(err)
	}
	if res := cloud.snapshot(); len(res) != 0 {
		t.Fatalf("a transient failure posted a result: %v", res)
	}
	if _, found, _ := data.NewDirectiveResultsRepo(db).Get(context.Background(), "dir-co-1"); found {
		t.Fatal("a transient failure was stored as the final result")
	}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatal(err)
	}
	res := cloud.snapshot()
	if calls != 2 || len(res) != 1 || res[0]["status"] != "applied" {
		t.Fatalf("calls=%d results=%v", calls, res)
	}
}

// A payload the decoder refuses is final: failed, stored, posted.
func TestTick_ChannelOrderDecodeRefusalIsFinal(t *testing.T) {
	p := fixturePayload(t)
	p["currency"] = "gbp"
	dir := map[string]any{"id": "dir-co-2", "type": "channel_order", "payload": p}
	cloud := &guestCloud{batches: [][]map[string]any{{dir}}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, "co_final.db").DB
	hooks := Hooks{ApplyChannelOrder: func(context.Context, ChannelOrderDirective) (string, error) { return "x", nil }}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatal(err)
	}
	if res := cloud.snapshot(); len(res) != 1 || res[0]["status"] != "failed" {
		t.Fatalf("results = %v", res)
	}
	if _, found, _ := data.NewDirectiveResultsRepo(db).Get(context.Background(), "dir-co-2"); !found {
		t.Fatal("final refusal not stored")
	}
}
