package print

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func sampleDoc() Doc {
	return Doc{
		StoreName: "Test Shop",
		Meta:      []string{"Receipt R-0001", "2026-07-14 12:00"},
		Lines: []Line{
			{Name: "Coca-Cola Can 330ml", Qty: "2", Amount: "£2.80"},
			{Name: "A very long product name that will not fit on one line at all", Qty: "1", Amount: "£1.00"},
		},
		Totals: []KV{
			{Label: "Subtotal", Amount: "£3.80"},
			{Label: "TOTAL", Amount: "£3.80", Strong: true},
		},
		Payments: []KV{{Label: "Cash", Amount: "£5.00"}, {Label: "Change", Amount: "£1.20"}},
		Footer:   []string{"Thank you!"},
	}
}

func TestRenderStructure(t *testing.T) {
	out := Render(sampleDoc())
	if !bytes.HasPrefix(out, cmdInit) {
		t.Error("stream must start with ESC @ init")
	}
	if !bytes.HasSuffix(out, cmdFeedCut) {
		t.Error("stream must end with feed+cut")
	}
	if bytes.Contains(out, cmdKickDrawer) {
		t.Error("drawer must not kick unless asked")
	}
	if !bytes.Contains(out, []byte("2 x Coca-Cola Can 330ml")) {
		t.Error("qty x name row missing")
	}
	// Amount right-aligned on a 42-char row.
	rows := strings.Split(string(out), "\n")
	found := false
	for _, r := range rows {
		if strings.Contains(r, "2 x Coca-Cola") && strings.HasSuffix(r, "£2.80") && len([]byte(r)) >= Width {
			found = true
		}
	}
	if !found {
		t.Error("line row not right-aligned to width")
	}
}

func TestRenderKickDrawer(t *testing.T) {
	d := sampleDoc()
	d.KickDrawer = true
	if !bytes.Contains(Render(d), cmdKickDrawer) {
		t.Error("drawer kick missing")
	}
}

func TestLongNameWraps(t *testing.T) {
	out := string(Render(sampleDoc()))
	if !strings.Contains(out, "A very long product name") {
		t.Error("long name row missing")
	}
	// its amount lands on its own right-aligned row
	if !strings.Contains(out, strings.Repeat(" ", Width-len("£1.00"))+"£1.00") {
		t.Error("wrapped amount row missing")
	}
}

func TestAsciiCharset(t *testing.T) {
	d := Doc{StoreName: "Café Zürich", Charset: "ascii",
		Totals: []KV{{Label: "TOTAL", Amount: "£1.00", Strong: true}}}
	out := string(Render(d))
	if !strings.Contains(out, "Cafe Zurich") {
		t.Errorf("diacritics should fold to ASCII, got %q", out)
	}
	if strings.Contains(out, "£") {
		t.Error("ascii mode must not emit multi-byte runes")
	}
	if !strings.Contains(out, "?1.00") {
		t.Error("unmappable runes should become ?")
	}
}

func TestUTF8PassThrough(t *testing.T) {
	d := Doc{StoreName: "فروشگاه", Totals: []KV{{Label: "جمع", Amount: "۱۲۰"}}}
	if !bytes.Contains(Render(d), []byte("فروشگاه")) {
		t.Error("utf8 mode must pass raw bytes through")
	}
}

func TestNewTransport(t *testing.T) {
	if tr, err := NewTransport(Config{Mode: "off"}); err != nil || tr != nil {
		t.Errorf("off mode: %v %v", tr, err)
	}
	if _, err := NewTransport(Config{Mode: "network"}); err == nil {
		t.Error("network without address must error")
	}
	if _, err := NewTransport(Config{Mode: "device"}); err == nil {
		t.Error("device without path must error")
	}
	if _, err := NewTransport(Config{Mode: "bogus"}); err == nil {
		t.Error("unknown mode must error")
	}
	tr, err := NewTransport(Config{Mode: "network", Address: "10.0.0.5"})
	if err != nil || !strings.Contains(tr.Describe(), "10.0.0.5:9100") {
		t.Errorf("default port not applied: %v %v", tr, err)
	}
}

func TestNetworkTransportDelivers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	received := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64*1024)
		n, _ := conn.Read(buf)
		received <- buf[:n]
	}()

	tr, err := NewTransport(Config{Mode: "network", Address: ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	data := Render(sampleDoc())
	if err := tr.Print(context.Background(), data); err != nil {
		t.Fatalf("print: %v", err)
	}
	select {
	case got := <-received:
		if !bytes.HasPrefix(got, cmdInit) {
			t.Error("printer did not receive the init sequence")
		}
		if !bytes.Contains(got, []byte("Coca-Cola")) {
			t.Error("printer did not receive the sale line")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("printer never received data")
	}
}

func TestNetworkTransportUnreachableFailsFast(t *testing.T) {
	tr, err := NewTransport(Config{Mode: "network", Address: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := tr.Print(ctx, []byte("x")); err == nil {
		t.Fatal("unreachable printer must error")
	}
	if time.Since(start) > 6*time.Second {
		t.Error("failure took too long — would stall the print goroutine")
	}
}
