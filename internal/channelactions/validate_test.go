package channelactions

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Shipment validation mirrors reference/contracts/channel-order-action-v1.json
// (ut-docs#4768): these values go to a customer-facing sales channel.

type vec struct {
	name   string
	ship   data.ChannelShipment
	field  string
	msgKey string
}

func shipVectors() []vec {
	ok := func(u string) data.ChannelShipment {
		return data.ChannelShipment{Carrier: "Royal Mail", TrackingURL: u}
	}
	badURL := func(u string) vec {
		return vec{name: "url " + u, ship: ok(u), field: "tracking_url", msgKey: "channel_orders.error.tracking_url_invalid"}
	}
	long := "https://a.example/" + strings.Repeat("x", 513-len("https://a.example/"))
	return []vec{
		{name: "plain", ship: data.ChannelShipment{Carrier: "Royal Mail"}},
		{name: "all fields", ship: data.ChannelShipment{Carrier: "Royal Mail", TrackingNumber: "AB123456789GB", TrackingURL: "https://tracking.example/AB123456789GB"}},
		{name: "url with query and fragment", ship: ok("https://t.example/a/b?x=1&y=%20#frag")},
		{name: "url with port", ship: ok("https://t.example:8443/a")},
		{name: "url host only", ship: ok("https://t.example")},
		{name: "carrier 64 multibyte runes", ship: data.ChannelShipment{Carrier: strings.Repeat("é", 64)}},
		{name: "tracking number 64", ship: data.ChannelShipment{Carrier: "X", TrackingNumber: strings.Repeat("A", 64)}},
		{name: "tracking number punctuation", ship: data.ChannelShipment{Carrier: "X", TrackingNumber: "AB 12.3_4/5-6"}},
		badURL("https://a.example/%zz"),
		badURL("http://a.example/x"),
		badURL("https://1.2.3.4/x"),
		badURL("https://1.2.3/x"),
		badURL("https://user@a.example"),
		badURL("https://user:pw@a.example/x"),
		badURL("https://localhost/x"),
		badURL("https://LOCALHOST/x"),
		badURL("https://a.localhost/x"),
		badURL("https://a.example/[x]"),
		badURL("https://[::1]/x"),
		badURL("https://a.example/x#a#b"),
		badURL("https://a.example?x"),
		badURL("https://a.example/x y"),
		badURL("https://a.example/x\"y"),
		badURL("https://a.example/é"),
		badURL("https://single/x"),
		badURL("https://a.example/x\n"),
		badURL("ftp://a.example/x"),
		badURL(long),
		{name: "carrier empty", ship: data.ChannelShipment{}, field: "carrier", msgKey: "channel_orders.error.carrier_required"},
		{name: "carrier 65 runes", ship: data.ChannelShipment{Carrier: strings.Repeat("a", 65)}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "carrier bidi U+202E", ship: data.ChannelShipment{Carrier: "Royal\u202eMail"}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "carrier C1 U+0085", ship: data.ChannelShipment{Carrier: "Royal\u0085Mail"}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "carrier newline", ship: data.ChannelShipment{Carrier: "Royal\nMail"}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "carrier soft hyphen", ship: data.ChannelShipment{Carrier: "Royal\u00adMail"}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "carrier zero width", ship: data.ChannelShipment{Carrier: "Royal\u200bMail"}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "carrier BOM", ship: data.ChannelShipment{Carrier: "\ufeffRoyal"}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "carrier invalid utf8", ship: data.ChannelShipment{Carrier: "Royal\xffMail"}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "carrier other Cf (U+E0001)", ship: data.ChannelShipment{Carrier: "Royal\U000e0001Mail"}, field: "carrier", msgKey: "channel_orders.error.carrier_invalid"},
		{name: "tracking number hash", ship: data.ChannelShipment{Carrier: "X", TrackingNumber: "AB#1"}, field: "tracking_number", msgKey: "channel_orders.error.tracking_number_invalid"},
		{name: "tracking number 65", ship: data.ChannelShipment{Carrier: "X", TrackingNumber: strings.Repeat("A", 65)}, field: "tracking_number", msgKey: "channel_orders.error.tracking_number_invalid"},
		{name: "tracking number non-ascii", ship: data.ChannelShipment{Carrier: "X", TrackingNumber: "ÄB1"}, field: "tracking_number", msgKey: "channel_orders.error.tracking_number_invalid"},
	}
}

func TestValidateShipment_Vectors(t *testing.T) {
	for _, v := range shipVectors() {
		field, key := ValidateShipment(v.ship)
		if field != v.field || key != v.msgKey {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", v.name, field, key, v.field, v.msgKey)
		}
	}
}

func TestValidateShipment_CheckOrder(t *testing.T) {
	// The first failing field in form order wins: carrier, number, link.
	f, _ := ValidateShipment(data.ChannelShipment{TrackingNumber: "#", TrackingURL: "http://x"})
	if f != "carrier" {
		t.Fatalf("field = %q", f)
	}
	f, _ = ValidateShipment(data.ChannelShipment{Carrier: "X", TrackingNumber: "#", TrackingURL: "http://x"})
	if f != "tracking_number" {
		t.Fatalf("field = %q", f)
	}
}

func TestValidCancelReason(t *testing.T) {
	for _, r := range []string{"customer_request", "out_of_stock", "fraud", "other"} {
		if !ValidCancelReason(r) {
			t.Errorf("%s refused", r)
		}
	}
	for _, r := range []string{"", "Other", "refund", " other"} {
		if ValidCancelReason(r) {
			t.Errorf("%q accepted", r)
		}
	}
}

func TestPrintable(t *testing.T) {
	for _, s := range []string{"Royal Mail", "Österreichische Post", "بريد", "e\u0301"} {
		if !Printable(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, r := range []rune{0x00, 0x1f, 0x7f, 0x80, 0x9f, 0xad, 0x200b, 0x200f, 0x2028, 0x202e, 0x2060, 0x2069, 0xfeff, 0x061c} {
		if Printable("a" + string(r) + "b") {
			t.Errorf("U+%04X accepted", r)
		}
	}
}

// --- the schema's own patterns ---------------------------------------------

type schemaDoc struct {
	Defs struct {
		Text struct {
			Pattern string `json:"pattern"`
		} `json:"text"`
		Action struct {
			Properties struct {
				Shipment struct {
					Properties struct {
						TrackingNumber struct {
							AllOf []struct {
								Pattern string `json:"pattern"`
							} `json:"allOf"`
						} `json:"tracking_number"`
						TrackingURL struct {
							Pattern string `json:"pattern"`
						} `json:"tracking_url"`
					} `json:"properties"`
				} `json:"shipment"`
				CreatedAt struct {
					Pattern string `json:"pattern"`
				} `json:"created_at"`
			} `json:"properties"`
		} `json:"action"`
	} `json:"$defs"`
}

func readSchema(t *testing.T) schemaDoc {
	t.Helper()
	b, err := os.ReadFile("testdata/channel-order-action-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var s schemaDoc
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// T-schema-pattern-pin: the schema's patterns are pinned as literals, so any
// change to the contract fails here and a person looks at validate.go.
func TestSchemaPatternsArePinned(t *testing.T) {
	s := readSchema(t)
	const (
		text = `^[^\u0000-\u001f\u007f-\u009f\u00ad\u200b-\u200f\u2028-\u202e\u2060-\u2069\ufeff]+$`
		num  = `^[A-Za-z0-9 ._/-]+$`
		url  = `^https://(?!localhost([:/]|$)|[^/:]*\.localhost([:/]|$)|[^/]*@)(?![0-9.]+([:/]|$)|\[)[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+(:[0-9]+)?(/([A-Za-z0-9._~!$&'()*+,;=:@/-]|%[0-9A-Fa-f]{2})*(\?([A-Za-z0-9._~!$&'()*+,;=:@/?-]|%[0-9A-Fa-f]{2})*)?(#([A-Za-z0-9._~!$&'()*+,;=:@/?-]|%[0-9A-Fa-f]{2})*)?)?$`
		at   = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z$`
	)
	p := s.Defs.Action.Properties
	got := map[string]string{"text": s.Defs.Text.Pattern, "url": p.Shipment.Properties.TrackingURL.Pattern, "created_at": p.CreatedAt.Pattern}
	for _, a := range p.Shipment.Properties.TrackingNumber.AllOf {
		if a.Pattern != "" {
			got["num"] = a.Pattern
		}
	}
	want := map[string]string{"text": text, "url": url, "num": num, "created_at": at}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("the contract's %s pattern changed: revisit validate.go (and the node comparison) before updating this pin.\n got %s\nwant %s", k, got[k], w)
		}
	}
}

// T-schema-node: run the schema's own patterns (ECMAScript, `u` flag) over the
// same vectors under node. The Go validator may be STRICTER than the schema
// (it also refuses every Unicode Cf, a dotted-hex host the schema lets
// through); it must never be looser.
func TestValidateShipment_NeverLooserThanTheSchemaPatterns(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	s := readSchema(t)
	var numPat string
	for _, a := range s.Defs.Action.Properties.Shipment.Properties.TrackingNumber.AllOf {
		if a.Pattern != "" {
			numPat = a.Pattern
		}
	}
	type probe struct{ Pattern, Value string }
	var probes []probe
	var vs []vec
	for _, v := range shipVectors() {
		vs = append(vs, v)
		probes = append(probes,
			probe{s.Defs.Text.Pattern, v.ship.Carrier},
			probe{numPat, v.ship.TrackingNumber},
			probe{s.Defs.Action.Properties.Shipment.Properties.TrackingURL.Pattern, v.ship.TrackingURL})
	}
	in, _ := json.Marshal(probes)
	cmd := exec.Command(node, "-e", `
const probes = JSON.parse(require('fs').readFileSync(0, 'utf8'));
console.log(JSON.stringify(probes.map(p => new RegExp(p.Pattern, 'u').test(p.Value))));`)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var verdicts []bool
	if err := json.Unmarshal(out, &verdicts); err != nil || len(verdicts) != len(probes) {
		t.Fatalf("node output: %v %s", err, out)
	}
	for i, v := range vs {
		jsCarrier, jsNum, jsURL := verdicts[3*i], verdicts[3*i+1], verdicts[3*i+2]
		field, _ := ValidateShipment(v.ship)
		// The first failing field masks later ones: compare only when Go accepted.
		if field == "" && (!jsCarrier || (v.ship.TrackingNumber != "" && !jsNum) || (v.ship.TrackingURL != "" && !jsURL)) {
			t.Errorf("%s: Go accepts what the schema pattern refuses (carrier %v, number %v, url %v)", v.name, jsCarrier, jsNum, jsURL)
		}
		// The contract's refused URL vectors are refused by the schema's pattern
		// too (the 513-char one is maxLength, not pattern), so they are the
		// contract's vectors and not only ours.
		if v.field == "tracking_url" && jsURL && len(v.ship.TrackingURL) != 513 {
			t.Errorf("%s: the schema pattern accepts a vector the contract names as refused", v.name)
		}
	}
}
