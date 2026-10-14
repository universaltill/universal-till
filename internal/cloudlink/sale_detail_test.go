package cloudlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/fleetlink"
)

// fixtureSaleDetail is a sale with n lines shaped like a real one: a
// product name, sku, catalogue ids, a modifier.
func fixtureSaleDetail(n int) SaleDetail {
	d := SaleDetail{
		ID: "sale-1", ReceiptNo: "R-000123", DisplayNo: "123", CreatedAt: "2026-10-14T09:30:00Z",
		Status: "completed", SaleType: "sale", Currency: "GBP",
		SubtotalMinor: 1000, TaxTotalMinor: 200, TotalMinor: 1200,
		CashierName: "Sam", RegisterID: "reg-1", RegisterName: "Front Till",
		Lines:    make([]SaleDetailLine, 0, n),
		Payments: []SaleDetailPayment{{Method: "cash", AmountMinor: 1200}, {Method: "card", AmountMinor: 0}},
	}
	for i := range n {
		d.Lines = append(d.Lines, SaleDetailLine{
			Name: fmt.Sprintf("Flat white, oat, large %03d", i), SKU: fmt.Sprintf("SKU-%06d", i),
			ItemID: fmt.Sprintf("item-%08d", i), VariantID: fmt.Sprintf("var-%08d", i),
			Qty: 1.5, UnitPriceMinor: 350, TaxRateBP: 2000, TaxMinor: 58, LineTotalMinor: 525,
			Modifiers: []string{"Extra shot"},
		})
	}
	return d
}

// frameLen is the encoded envelope the till sends as the reply, with the
// longest ids fleetlink and the cloud mint ("t"/"c" + 8 hex + "-" + up to
// 13 base36 digits = 23 bytes).
func frameLen(t *testing.T, d SaleDetail) int {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("z", 23)
	b, err := fleetlink.Encode(fleetlink.Envelope{ID: id, Type: typeSaleDetail, ReplyTo: id, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// ADR-0155 §4: a long sale is trimmed from the end until the whole frame
// is ≤ 15 KiB; the header and payments stay, lines_omitted counts the rest.
func TestFitSaleDetailTrimsLinesToTheFrameBudget(t *testing.T) {
	d := fixtureSaleDetail(500)
	if frameLen(t, d) <= maxSaleDetailFrame {
		t.Fatal("fixture too small to exercise the trim")
	}
	got, ok := fitSaleDetail(d)
	if !ok {
		t.Fatal("fitSaleDetail refused a sale whose header and payments fit")
	}
	if n := frameLen(t, got); n > maxSaleDetailFrame || n > 16<<10 {
		t.Fatalf("frame = %d bytes, want ≤ %d", n, maxSaleDetailFrame)
	}
	if got.LinesOmitted == 0 || len(got.Lines)+got.LinesOmitted != 500 {
		t.Fatalf("lines = %d, omitted = %d, want a trim that accounts for all 500", len(got.Lines), got.LinesOmitted)
	}
	if got.Lines[0].Name != d.Lines[0].Name {
		t.Fatal("trim dropped from the front; it must drop from the end")
	}
	// The largest prefix that fits: one more line would be over.
	more := got
	more.Lines = d.Lines[:len(got.Lines)+1]
	more.LinesOmitted = got.LinesOmitted - 1
	if frameLen(t, more) <= maxSaleDetailFrame {
		t.Fatalf("kept %d lines but %d still fit", len(got.Lines), len(more.Lines))
	}
	if len(got.Payments) != 2 || got.TotalMinor != 1200 || got.CashierName != "Sam" {
		t.Fatalf("header/payments changed by the trim: %+v", got)
	}

	// A small sale goes out whole.
	small, ok := fitSaleDetail(fixtureSaleDetail(3))
	if !ok || len(small.Lines) != 3 || small.LinesOmitted != 0 {
		t.Fatalf("small sale = %d lines, %d omitted, ok %v; want untouched", len(small.Lines), small.LinesOmitted, ok)
	}
}

// ADR-0155 §4: over budget with no lines left → too_large (refused).
func TestFitSaleDetailRefusesWhenPaymentsAloneAreTooLarge(t *testing.T) {
	d := fixtureSaleDetail(5)
	for range 1000 {
		d.Payments = append(d.Payments, SaleDetailPayment{Method: "voucher", AmountMinor: 1})
	}
	if _, ok := fitSaleDetail(d); ok {
		t.Fatal("fitSaleDetail accepted a frame over budget with zero lines")
	}
}

// ADR-0155 §4: the till answers at most 5 requests/s, burst 10.
func TestSaleDetailTokenBucket(t *testing.T) {
	c := New(Options{})
	now := time.Unix(1_700_000_000, 0)
	for i := range detailBurst {
		if !c.takeDetailToken(now) {
			t.Fatalf("request %d of the burst refused", i+1)
		}
	}
	if c.takeDetailToken(now) {
		t.Fatal("request past the burst admitted")
	}
	if !c.takeDetailToken(now.Add(detailEvery)) {
		t.Fatal("no token refilled after one interval")
	}
	if c.takeDetailToken(now.Add(detailEvery)) {
		t.Fatal("more than one token refilled after one interval")
	}
}

func remoteCode(t *testing.T, err error) *fleetlink.RemoteError {
	t.Helper()
	var re *fleetlink.RemoteError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want a *fleetlink.RemoteError", err)
	}
	return re
}

// ADR-0155 §1: the handler's own codes — not_found (final), rate_limited
// (retryable), too_large (final) — and a sale_detail reply otherwise.
func TestAnswerSaleDetailCodes(t *testing.T) {
	var calls atomic.Int32
	c := New(Options{SaleDetail: func(_ context.Context, id string) (SaleDetail, bool, error) {
		calls.Add(1)
		switch id {
		case "missing":
			return SaleDetail{}, false, nil
		case "huge":
			d := fixtureSaleDetail(0)
			for range 2000 {
				d.Payments = append(d.Payments, SaleDetailPayment{Method: "voucher", AmountMinor: 1})
			}
			return d, true, nil
		}
		return fixtureSaleDetail(2), true, nil
	}})
	ask := func(id string) (any, error) {
		raw, _ := json.Marshal(map[string]string{"sale_id": id})
		return c.answerSaleDetail(context.Background(), "cloud", raw)
	}

	res, err := ask("sale-1")
	if err != nil {
		t.Fatal(err)
	}
	rep, ok := res.(fleetlink.Reply)
	if !ok || rep.Type != "sale_detail" {
		t.Fatalf("result = %#v, want a sale_detail Reply", res)
	}
	if re := remoteCode(t, func() error { _, err := ask("missing"); return err }()); re.Code != "not_found" || re.Retryable {
		t.Fatalf("missing sale = %+v, want not_found, not retryable", re)
	}
	if re := remoteCode(t, func() error { _, err := ask("huge"); return err }()); re.Code != "too_large" || re.Retryable {
		t.Fatalf("huge sale = %+v, want too_large, not retryable", re)
	}
	if re := remoteCode(t, func() error {
		_, err := c.answerSaleDetail(context.Background(), "cloud", json.RawMessage(`{"sale_id":""}`))
		return err
	}()); re.Code != fleetlink.CodeBadMessage {
		t.Fatalf("empty sale_id = %+v, want bad_message", re)
	}

	// The burst is spent (3 above + 7 more — the bad_message spent none);
	// the 11th is rate_limited and never reaches the lookup.
	for i := range 7 {
		if _, err := ask("sale-1"); err != nil {
			t.Fatalf("request %d inside the burst: %v", 4+i, err)
		}
	}
	before := calls.Load()
	if re := remoteCode(t, func() error { _, err := ask("sale-1"); return err }()); re.Code != "rate_limited" || !re.Retryable {
		t.Fatalf("over the rate = %+v, want rate_limited, retryable", re)
	}
	if calls.Load() != before {
		t.Fatal("a rate-limited request still read the sale")
	}

	// A lookup failure is fleetlink's internal (a plain error).
	c2 := New(Options{SaleDetail: func(context.Context, string) (SaleDetail, bool, error) {
		return SaleDetail{}, false, errors.New("db gone")
	}})
	if _, err := c2.answerSaleDetail(context.Background(), "cloud", json.RawMessage(`{"sale_id":"x"}`)); err == nil || errors.As(err, new(*fleetlink.RemoteError)) {
		t.Fatalf("lookup failure = %v, want a plain error (internal)", err)
	}
}

// ADR-0155 §4: the cloud link's handler deadline is 3 s, explicitly.
func TestCloudLinkRequestTimeoutIsThreeSeconds(t *testing.T) {
	if got := DefaultConfig().RequestTimeout; got != 3*time.Second {
		t.Fatalf("DefaultConfig().RequestTimeout = %v, want 3s", got)
	}
	if got := withConfigDefaults(fleetlink.Config{}).RequestTimeout; got != 3*time.Second {
		t.Fatalf("withConfigDefaults(zero).RequestTimeout = %v, want 3s", got)
	}
}

// End to end over the socket (ADR-0155 §1/§2/§4): the hello carries
// caps ["sale_detail"], a sale_detail_request gets a sale_detail reply
// with reply_to, a 500-line sale arrives trimmed under the frame budget,
// and an unknown sale is error not_found.
func TestSaleDetailRequestOverTheLink(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.SaleDetail = func(_ context.Context, id string) (SaleDetail, bool, error) {
			switch id {
			case "sale-1":
				return fixtureSaleDetail(2), true, nil
			case "long":
				return fixtureSaleDetail(500), true, nil
			}
			return SaleDetail{}, false, nil
		}
	})
	c := h.cloud.nextConn(t)
	var hello struct {
		Caps []string `json:"caps"`
	}
	if err := json.Unmarshal(c.frame(t, "hello").Payload, &hello); err != nil {
		t.Fatal(err)
	}
	if len(hello.Caps) != 1 || hello.Caps[0] != "sale_detail" {
		t.Fatalf("hello caps = %v, want [sale_detail]", hello.Caps)
	}

	c.request("c-q1", "sale_detail_request", map[string]string{"sale_id": "sale-1"})
	rep := c.reply(t, "c-q1")
	if rep.Type != "sale_detail" {
		t.Fatalf("reply = %s %s, want sale_detail", rep.Type, rep.Payload)
	}
	var got map[string]any
	if err := json.Unmarshal(rep.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "sale-1" || got["total_minor"] != 1200.0 || got["lines_omitted"] != 0.0 {
		t.Fatalf("sale_detail payload = %s", rep.Payload)
	}
	if lines, _ := got["lines"].([]any); len(lines) != 2 {
		t.Fatalf("lines = %v, want 2", got["lines"])
	}

	c.request("c-q2", "sale_detail_request", map[string]string{"sale_id": "long"})
	rep = c.reply(t, "c-q2")
	b, _ := fleetlink.Encode(rep)
	if rep.Type != "sale_detail" || len(b) > maxSaleDetailFrame {
		t.Fatalf("long sale reply = %s, %d bytes; want sale_detail ≤ %d", rep.Type, len(b), maxSaleDetailFrame)
	}
	var long SaleDetail
	if err := json.Unmarshal(rep.Payload, &long); err != nil {
		t.Fatal(err)
	}
	if long.LinesOmitted == 0 || len(long.Lines)+long.LinesOmitted != 500 {
		t.Fatalf("long sale: %d lines, %d omitted", len(long.Lines), long.LinesOmitted)
	}

	c.request("c-q3", "sale_detail_request", map[string]string{"sale_id": "nope"})
	rep = c.reply(t, "c-q3")
	if rep.Type != "error" || string(rep.Payload) != `{"code":"not_found","retryable":false}` {
		t.Fatalf("unknown sale reply = %s %s, want error not_found", rep.Type, rep.Payload)
	}
}

// A till with no sale lookup wired neither advertises the cap nor answers
// the request (unknown_type, as before ADR-0155).
func TestNoSaleDetailCapWithoutLookup(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	if p := c.frame(t, "hello").Payload; strings.Contains(string(p), "caps") {
		t.Fatalf("hello = %s, want no caps without a lookup", p)
	}
	c.request("c-q1", "sale_detail_request", map[string]string{"sale_id": "sale-1"})
	if rep := c.reply(t, "c-q1"); rep.Type != "error" || !strings.Contains(string(rep.Payload), "unknown_type") {
		t.Fatalf("reply = %s %s, want unknown_type", rep.Type, rep.Payload)
	}
}
