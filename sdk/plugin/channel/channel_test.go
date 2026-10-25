//go:build !wasip1

package channel

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// All data is invented.

func TestSubmit_ListingRoundTripAndPerRecordResults(t *testing.T) {
	h := NewFakeHost()
	h.Reject = func(kind, id string) *SubmitResult {
		if id == "p-2" {
			return &SubmitResult{ExternalID: id, Status: ResultRejected, Reason: "bad_price"}
		}
		return nil
	}
	UseFakeHost(t, h)
	listings := []Listing{
		{Identity: Identity{ExternalID: "p-1", ExternalVersion: "2026-10-22T09:00:00Z"}, Title: "Linen shirt",
			Variants: []Variant{{ExternalID: "v-1", SKU: "LS-M-BLU", PriceMinor: 3500, Currency: "GBP", Tracked: true}}},
		{Identity: Identity{ExternalID: "p-2"}, Title: "Odd"},
	}
	res, err := Submit(KindListing, listings)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Status != ResultAccepted || res[1].Status != ResultRejected || res[1].Reason != "bad_price" {
		t.Fatalf("results %+v", res)
	}
	if len(h.Submitted) != 1 || h.Submitted[0].Kind != "listing" || len(h.Submitted[0].Records) != 2 {
		t.Fatalf("host saw %+v", h.Submitted)
	}
	var back Listing
	if err := json.Unmarshal(h.Submitted[0].Records[0], &back); err != nil || back.Variants[0].PriceMinor != 3500 || back.ExternalID != "p-1" {
		t.Fatalf("record did not survive the wire: %+v %v", back, err)
	}
}

func TestSubmit_RefusesMoreThanFiveHundredRecords(t *testing.T) {
	recs := make([]Category, MaxBatchRecords+1)
	h := NewFakeHost()
	UseFakeHost(t, h)
	if _, err := Submit(KindCategory, recs); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if h.Calls[FnSubmit] != 0 {
		t.Fatal("the SDK must refuse an over-large batch before it reaches the host")
	}
	if _, err := Submit(KindCategory, "not a slice"); err == nil {
		t.Fatal("a non-slice must be refused")
	}
}

func TestSubmit_UnknownKindIsInvalid(t *testing.T) {
	UseFakeHost(t, NewFakeHost())
	if _, err := Submit("delete", []Category{{Identity: Identity{ExternalID: "c"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestOrderCarriesNoPersonalFields(t *testing.T) {
	// The wire types of an order have no field for a customer's name,
	// address, email or phone: they are fetched live through AskContact and
	// never stored. Walk the JSON tags of every type an order is made of.
	// The one exemption mirrors the host gate (contract 9.3): `name` directly
	// on an element of the top-level `lines` array, the product title. A
	// `name` anywhere else (the customer included) still fails.
	var walk func(rt reflect.Type, path string)
	seen := map[reflect.Type]bool{}
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			productTitle := path == "Order.Lines" && rt == reflect.TypeOf(OrderLine{}) && tag == "name"
			for _, banned := range []string{"name", "email", "phone", "mobile", "address", "street", "city", "postcode", "postal_code", "zip"} {
				if strings.Contains(tag, banned) && !productTitle {
					t.Errorf("%s.%s has json tag %q: personal data does not belong in a submitted order", path, f.Name, tag)
				}
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	for _, v := range []any{Order{}, OrderUpdate{}, Refund{}, Customer{}} {
		walk(reflect.TypeOf(v), reflect.TypeOf(v).Name())
	}
	if !seen[reflect.TypeOf(OrderLine{})] {
		t.Fatal("the walk never reached OrderLine: the exemption would be untested")
	}
}

// Contract 2 "Required fields of a submitted order record" and the cloud's
// channelorder.Parse: a required number or boolean at zero/false must still
// be on the wire, or the cloud rejects the record as invalid_order.
func TestOrderZeroValuesKeepEveryRequiredKey(t *testing.T) {
	o := Order{
		Identity: Identity{ExternalID: "o-1", ExternalVersion: "1"},
		PlacedAt: "2026-10-22T10:12:00Z", Currency: "GBP", Status: "new",
		Lines: []OrderLine{{ExternalID: "l-1", Qty: 1}},
	}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"external_id", "external_version", "order_number", "placed_at", "currency", "status", "lines",
		"shipping_minor", "shipping_tax_bp", "shipping_tax_included", "order_discount_minor", "tax_added_minor", "refund_total_minor", "total_minor"} {
		if _, ok := got[k]; !ok {
			t.Errorf("order key %q missing from %s", k, b)
		}
	}
	for _, k := range []string{"payment_state", "customer", "cloud_id"} {
		if _, ok := got[k]; ok {
			t.Errorf("optional order key %q sent when unset: %s", k, b)
		}
	}
	lines, _ := got["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("lines: %s", b)
	}
	line, _ := lines[0].(map[string]any)
	for _, k := range []string{"external_id", "sku", "name", "qty", "unit_price_minor", "tax_bp", "tax_included", "discount_line_minor", "refunded_quantity", "restocked_quantity"} {
		if _, ok := line[k]; !ok {
			t.Errorf("line key %q missing from %s", k, b)
		}
	}
	if _, ok := line["variant_external_id"]; ok {
		t.Errorf("optional variant_external_id sent when unset: %s", b)
	}
}

// The contract section 2 order example (invented data) must decode into Order
// without an unknown field and encode back to the same JSON.
func TestOrderContractExampleRoundTrips(t *testing.T) {
	const example = `{
  "external_id":"o-9001","external_version":"2026-10-22T10:12:00Z",
  "order_number":"#1001","placed_at":"2026-10-22T10:12:00Z","currency":"GBP",
  "status":"new","payment_state":"paid",
  "lines":[{"external_id":"l-1","variant_external_id":"v-1","sku":"LS-M-BLU",
    "name":"Linen shirt","qty":2,"unit_price_minor":3500,"tax_bp":2000,
    "tax_included":true,"discount_line_minor":0,
    "refunded_quantity":0,"restocked_quantity":0}],
  "shipping_minor":399,"shipping_tax_bp":2000,"shipping_tax_included":true,
  "order_discount_minor":0,"tax_added_minor":0,"refund_total_minor":0,
  "total_minor":7399,
  "customer":{"external_id":"c-77","country":"GB"}
}`
	var o Order
	dec := json.NewDecoder(strings.NewReader(example))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		t.Fatal(err)
	}
	back, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal([]byte(example), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(back, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip differs:\nwant %v\n got %v", want, got)
	}
}

// Contract 9.1: order_update and refund are the order shape, not a delta.
func TestOrderUpdateAndRefundAreTheOrderShape(t *testing.T) {
	if reflect.TypeOf(OrderUpdate{}) != reflect.TypeOf(Order{}) || reflect.TypeOf(Refund{}) != reflect.TypeOf(Order{}) {
		t.Fatal("OrderUpdate and Refund must be the same type as Order (contract 9.1)")
	}
}

func TestHTTP_RoundTripAndBufferRetry(t *testing.T) {
	h := NewFakeHost()
	big := make([]byte, 700<<10) // over the 512 KiB first buffer once base64-encoded
	for i := range big {
		big[i] = 'a'
	}
	var seen HTTPRequest
	hookRuns := 0
	h.HTTP = func(r HTTPRequest) (HTTPResponse, error) {
		hookRuns++
		seen = r
		return HTTPResponse{Status: 200, Headers: map[string]string{"X-A": "b"}, Body: big}, nil
	}
	UseFakeHost(t, h)
	resp, err := HTTP(HTTPRequest{Method: "POST", URL: "https://a.demo-shop.example/api", Body: []byte(`{"q":1}`), CostHint: 12, Throttle: &Throttle{Available: 900, Maximum: 1000, RestoreRate: 50}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || len(resp.Body) != len(big) || resp.Headers["X-A"] != "b" {
		t.Fatalf("response %d %d", resp.Status, len(resp.Body))
	}
	if seen.Method != "POST" || string(seen.Body) != `{"q":1}` || seen.CostHint != 12 {
		t.Fatalf("request did not survive: %+v", seen)
	}
	if seen.Throttle == nil || *seen.Throttle != (Throttle{Available: 900, Maximum: 1000, RestoreRate: 50}) {
		t.Fatalf("throttle report did not survive: %+v", seen.Throttle)
	}
	if h.Calls[FnHTTP] != 2 {
		t.Fatalf("the oversize first answer is retried once (2 calls), got %d", h.Calls[FnHTTP])
	}
	if hookRuns != 1 {
		t.Fatalf("the retry must be answered from the host's cache: the request ran %d times", hookRuns)
	}
}

func TestHTTP_DeniedAndHostError(t *testing.T) {
	h := NewFakeHost()
	UseFakeHost(t, h)
	if _, err := HTTP(HTTPRequest{URL: "https://evil.example/"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("no hook means denied: %v", err)
	}
	h.HTTP = func(HTTPRequest) (HTTPResponse, error) { return HTTPResponse{}, ErrQuota }
	if _, err := HTTP(HTTPRequest{URL: "https://a.demo-shop.example/"}); !errors.Is(err, ErrQuota) {
		t.Fatalf("host error code lost: %v", err)
	}
}

func TestCursor_SetGetAndQuota(t *testing.T) {
	h := NewFakeHost()
	UseFakeHost(t, h)
	if _, err := CursorGet("orders"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := CursorSet("orders", []byte("2026-10-22T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	v, err := CursorGet("orders")
	if err != nil || string(v) != "2026-10-22T10:00:00Z" {
		t.Fatalf("got %q %v", v, err)
	}
	if err := CursorSet("big", make([]byte, MaxCursorBytes)); !errors.Is(err, ErrQuota) {
		t.Fatalf("over the 64 KiB total must be ErrQuota, got %v", err)
	}
	if err := CursorSet("", []byte("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty key: %v", err)
	}
}

func TestOutbox_NextAckAndNoDeleteOp(t *testing.T) {
	h := NewFakeHost()
	exp := int64(4)
	h.Outbox = []OutboxOp{
		{ID: "op-1", Kind: OpStockSet, ExternalID: "v-1", Expect: &exp, Payload: json.RawMessage(`{"qty":3}`)},
		{ID: "op-2", Kind: OpListingUnpublish, ExternalID: "p-1"},
	}
	UseFakeHost(t, h)
	ops, err := OutboxNext(10)
	if err != nil || len(ops) != 2 || *ops[0].Expect != 4 {
		t.Fatalf("ops %+v %v", ops, err)
	}
	if err := OutboxAck("op-1", Ack{ExternalID: "v-1", ExternalVersion: "9"}); err != nil {
		t.Fatal(err)
	}
	if err := OutboxAck("op-2", Ack{Error: &AckError{Code: "not_found", Retryable: false}}); err != nil {
		t.Fatal(err)
	}
	if err := OutboxAck("op-404", Ack{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown op: %v", err)
	}
	if a := h.Acks["op-2"]; a.Error == nil || a.Error.Retryable {
		t.Fatalf("dead-letter ack lost: %+v", a)
	}
	if _, err := OutboxNext(0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("max 0: %v", err)
	}
	// Never delete: the contract has no delete operation, so neither does the SDK.
	for _, c := range []string{OpListingUpsert, OpListingUnpublish, OpStockSet, OpFulfilmentCreate, OpOrderCancel, OpRefundCreate, OpImageUpsert} {
		if strings.Contains(c, "delete") || strings.Contains(c, "remove") {
			t.Fatalf("op %q deletes", c)
		}
	}
}

func TestLog_RecordsLevelCodeFields(t *testing.T) {
	h := NewFakeHost()
	UseFakeHost(t, h)
	Log(LevelWarn, "import.page", map[string]any{"page": 3, "n": 50})
	if len(h.Logs) != 1 || h.Logs[0].Level != LevelWarn || h.Logs[0].Code != "import.page" || h.Logs[0].Fields["page"].(float64) != 3 {
		t.Fatalf("logs %+v", h.Logs)
	}
}

func TestDispatch_RoutesAsksAndRefusesUnknown(t *testing.T) {
	h := Handlers{
		AskWebhook: func(e Event) (any, error) {
			var in WebhookInput
			if err := e.Decode(&in); err != nil {
				return nil, err
			}
			return WebhookResult{Account: "a.demo-shop.example", DedupeID: in.Headers["X-Id"], Event: EventOrderChanged, ResourceIDs: []string{"o-1"}}, nil
		},
	}
	ev, _ := json.Marshal(map[string]any{"id": "1", "type": AskWebhook, "payload": WebhookInput{Headers: map[string]string{"X-Id": "w-1"}, Body: []byte("{}")}})
	out, err := Dispatch(h, ev)
	if err != nil {
		t.Fatal(err)
	}
	var res WebhookResult
	if err := json.Unmarshal(out, &res); err != nil || res.DedupeID != "w-1" || res.Event != EventOrderChanged {
		t.Fatalf("answer %s %v", out, err)
	}
	if _, err := Dispatch(h, []byte(`{"type":"channel.tick"}`)); !errors.Is(err, ErrNoHandler) {
		t.Fatalf("an unhandled ask must be an error, got %v", err)
	}
	if _, err := Dispatch(h, []byte(`not json`)); err == nil {
		t.Fatal("garbage event must error")
	}
}

func TestWebhookResult_OrderIDsRoundTripAndOmitWhenEmpty(t *testing.T) {
	h := Handlers{
		AskWebhook: func(e Event) (any, error) {
			return WebhookResult{Account: "a", DedupeID: "d", Event: EventCustomerRedact, ResourceIDs: []string{"c-1"}, OrderIDs: []string{"o-1", "o-2"}}, nil
		},
	}
	out, err := Dispatch(h, []byte(`{"id":"1","type":"`+AskWebhook+`","payload":{"headers":{},"body":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	var res WebhookResult
	if err := json.Unmarshal(out, &res); err != nil || len(res.OrderIDs) != 2 || res.OrderIDs[1] != "o-2" {
		t.Fatalf("order_ids lost: %s %v", out, err)
	}
	empty, _ := json.Marshal(WebhookResult{Account: "a", DedupeID: "d", Event: EventOrderChanged})
	if strings.Contains(string(empty), "order_ids") {
		t.Fatalf("empty order_ids must be omitted: %s", empty)
	}
}

// The guest must link ONLY the "channel" module: importing the till's "ut"
// SDK would add `ut` imports (storage, raw http, settings, secrets) a channel
// guest must never have.
func TestPackageDoesNotImportTheUtSDK(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		for name, f := range p.Files {
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if strings.HasPrefix(path, "github.com/universaltill/universal-till/sdk/plugin") {
					t.Fatalf("%s imports %s: a channel guest must not link the till's ut module", name, path)
				}
			}
		}
	}
	raw, err := os.ReadFile("raw_wasip1.go")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)^//go:wasmimport (\S+) `).FindAllStringSubmatch(string(raw), -1) == nil {
		t.Fatal("no wasmimport found")
	}
	for _, m := range regexp.MustCompile(`(?m)^//go:wasmimport (\S+) `).FindAllStringSubmatch(string(raw), -1) {
		if m[1] != HostModule {
			t.Fatalf("wasmimport from module %q, only %q is allowed", m[1], HostModule)
		}
	}
}

// Every Fn constant has exactly one wasmimport and vice versa.
func TestWasmImportsMatchHostFunctionNames(t *testing.T) {
	raw, err := os.ReadFile("raw_wasip1.go")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range regexp.MustCompile(`(?m)^//go:wasmimport channel (\S+)`).FindAllStringSubmatch(string(raw), -1) {
		got = append(got, m[1])
	}
	want := []string{FnHTTP, FnCursorGet, FnCursorSet, FnSubmit, FnOutboxNext, FnOutboxAck, FnLog}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("wasmimports %v != host function names %v", got, want)
	}
}

func TestHTTP_NoThrottleStaysAbsentOnTheWire(t *testing.T) {
	b, err := json.Marshal(HTTPRequest{URL: "https://a.demo-shop.example/x", CostHint: 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "throttle") {
		t.Fatalf("an absent report must not be sent: %s", b)
	}
	b, _ = json.Marshal(wireHTTPRequest{URL: "u", Throttle: &Throttle{Available: 1, Maximum: 2, RestoreRate: 3}})
	if !strings.Contains(string(b), `"throttle":{"available":1,"maximum":2,"restore_rate":3}`) {
		t.Fatalf("wire shape: %s", b)
	}
}

// ut-docs#4904: a refund.create ack may report the refund money it moved; the
// field is omitted when unset, so an older host never sees it.
func TestAckRefundedAmountMinorIsOptionalOnTheWire(t *testing.T) {
	if b, _ := json.Marshal(Ack{ExternalID: "o-1"}); strings.Contains(string(b), "refunded_amount_minor") {
		t.Fatalf("an unset amount was sent: %s", b)
	}
	amount := int64(4000)
	b, _ := json.Marshal(Ack{ExternalID: "o-1", RefundedAmountMinor: &amount})
	if !strings.Contains(string(b), `"refunded_amount_minor":4000`) {
		t.Fatalf("the amount is not on the wire: %s", b)
	}
	h := NewFakeHost()
	h.Outbox = []OutboxOp{{ID: "op-r", Kind: OpRefundCreate}}
	UseFakeHost(t, h)
	if err := OutboxAck("op-r", Ack{ExternalID: "o-1", RefundedAmountMinor: &amount}); err != nil {
		t.Fatal(err)
	}
	if got := h.Acks["op-r"].RefundedAmountMinor; got == nil || *got != 4000 {
		t.Fatalf("the host did not receive the amount: %v", got)
	}
}

// ut-docs#4904: the fake host refuses an amount the cloud host refuses, so an
// adapter's unit tests see the same -4 the real host answers.
func TestFakeHostRefusesARefundedAmountTheCloudHostRefuses(t *testing.T) {
	neg, huge, ok := int64(-1), int64(1)<<50+1, int64(10)
	for name, a := range map[string]Ack{
		"negative":        {RefundedAmountMinor: &neg},
		"above 2^50":      {RefundedAmountMinor: &huge},
		"beside an error": {RefundedAmountMinor: &ok, Error: &AckError{Code: "refund_exceeds_ceiling"}},
	} {
		h := NewFakeHost()
		h.Outbox = []OutboxOp{{ID: "op-r", Kind: OpRefundCreate}}
		UseFakeHost(t, h)
		if err := OutboxAck("op-r", a); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}
