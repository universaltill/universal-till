package print

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleSlip(code, holder string) VoucherSlip {
	return VoucherSlip{
		StoreName: "Cafe Muster",
		Header:    []string{"Hauptstr. 1"},
		Charset:   "utf8",
		Voucher:   VoucherBlock{Label: "Gift voucher", Amount: "25.00", Holder: holder, Code: code, Issued: true},
	}
}

func TestRenderVoucherSlip_Content(t *testing.T) {
	s := sampleSlip("GV-7K3P", "For Anna")
	out := RenderVoucherSlip(s)
	if !bytes.HasPrefix(out, cmdInit) {
		t.Error("slip must start with ESC @")
	}
	for _, want := range []string{"Cafe Muster", "Hauptstr. 1", "Gift voucher", "25.00", "For Anna"} {
		if !bytes.Contains(out, encodeText(want, s.Charset)) {
			t.Errorf("slip lacks %q", want)
		}
	}
	data, _ := code128Data("GV-7K3P")
	if !bytes.Contains(out, append([]byte{0x1d, 0x6b, 0x49, byte(len(data))}, data...)) {
		t.Error("code not printed as CODE128")
	}
	if !bytes.Contains(out, cmdBarcodeHRI) {
		t.Error("HRI not enabled")
	}
	if !bytes.HasSuffix(out, cmdFeedCut) {
		t.Error("slip must end with feed+cut")
	}
	if bytes.Contains(out, cmdKickDrawer) || bytes.Contains(out, cmdKickDrawerPin5) {
		t.Error("slip must never kick the drawer")
	}
}

func TestRenderVoucherSlip_NotBarcodePrintable(t *testing.T) {
	s := sampleSlip("Gutschein-ü", "")
	out := RenderVoucherSlip(s)
	if bytes.Contains(out, cmdBarcodeCode128) {
		t.Error("unencodable code printed a barcode")
	}
	if !bytes.Contains(out, encodeText("Gutschein-ü", s.Charset)) {
		t.Error("code text missing")
	}
	if bytes.Contains(out, encodeText("For", s.Charset)) {
		t.Error("no holder: no For row")
	}
}

func TestRenderVoucherSlip_CharsetSelect(t *testing.T) {
	s := sampleSlip("GV-1", "")
	s.Charset = "cp858"
	out := RenderVoucherSlip(s)
	cmd := codepageSelectCmd("cp858")
	if cmd == nil {
		t.Fatal("cp858 has no select command")
	}
	if !bytes.HasPrefix(out, append(append([]byte{}, cmdInit...), cmd...)) {
		t.Error("codepage select must follow init")
	}
}

func TestRenderVoucherSlipText(t *testing.T) {
	s := sampleSlip("GV-7K3P", "For Anna")
	out := RenderVoucherSlipText(s)
	for _, want := range []string{"Cafe Muster", "Gift voucher", "25.00", "For Anna", "GV-7K3P"} {
		if !strings.Contains(out, want) {
			t.Errorf("text slip lacks %q", want)
		}
	}
	if strings.Contains(out, "║") {
		t.Error("text slip must not carry placeholder bars")
	}
}

func TestPrintDocWithSlips_OneJobInOrder(t *testing.T) {
	d := sampleDoc()
	a, b := sampleSlip("GV-A", "For Anna"), sampleSlip("GV-B", "")
	run := func(slips []VoucherSlip) []byte {
		path := filepath.Join(t.TempDir(), "lp0")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Config{Mode: "device", Device: path, Charset: d.Charset}
		if err := PrintDocWithSlips(context.Background(), cfg, d, slips); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := append(append(Render(d), RenderVoucherSlip(a)...), RenderVoucherSlip(b)...)
	if got := run([]VoucherSlip{a, b}); !bytes.Equal(got, want) {
		t.Error("job != receipt + slip A + slip B")
	}
	if got := run(nil); !bytes.Equal(got, Render(d)) {
		t.Error("nil slips must equal the plain receipt")
	}
}
