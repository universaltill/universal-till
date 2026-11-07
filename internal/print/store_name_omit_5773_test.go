package print

import (
	"bytes"
	"strings"
	"testing"
)

// ut-docs#5773: an unnamed shop prints no shop-name row (no blank gap, no
// stray double-width pair). Named output is the unnamed output plus exactly
// the row it always had.

const omitName = "Café Linde"

func omitCentred(s string) string {
	return strings.Repeat(" ", (Width-displayWidth(s))/2) + s + "\n"
}

func omitDoc(name string, header ...string) Doc {
	return Doc{
		StoreName: name,
		Header:    header,
		Lines:     []Line{{Name: "Tea", Qty: "1", Amount: "1.00"}},
		Totals:    []KV{{Label: "TOTAL", Amount: "1.00"}},
		Charset:   "utf8",
	}
}

func TestRender_NoStoreNameRow(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		unnamed := Render(omitDoc(blank, "1 High St"))
		if bytes.Contains(unnamed, cmdDoubleOn) {
			t.Errorf("name %q: unnamed receipt still has a double-width pair", blank)
		}
		named := Render(omitDoc(omitName, "1 High St"))
		row := append(append(append([]byte{}, cmdDoubleOn...), []byte(omitName+"\n")...), cmdDoubleOff...)
		if !bytes.Contains(named, row) {
			t.Fatalf("named receipt lost its name row")
		}
		if got := bytes.Replace(named, row, nil, 1); !bytes.Equal(got, unnamed) {
			t.Errorf("name %q: named minus its row != unnamed", blank)
		}
	}
}

func TestRenderText_NoStoreNameRow(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		unnamed := RenderText(omitDoc(blank, "1 High St"))
		if !strings.HasPrefix(unnamed, omitCentred("1 High St")) {
			t.Errorf("name %q: unnamed text should start with the header line, got %q", blank, unnamed[:20])
		}
		named := RenderText(omitDoc(omitName, "1 High St"))
		if named != omitCentred(omitName)+unnamed {
			t.Errorf("name %q: named != name row + unnamed", blank)
		}
	}
	noHeader := RenderText(omitDoc(""))
	if strings.HasPrefix(noHeader, "\n") || strings.HasPrefix(noHeader, " ") {
		t.Errorf("no leading blank line expected, got %q", noHeader[:10])
	}
}

func omitSlip(name string) VoucherSlip {
	return VoucherSlip{StoreName: name, Header: []string{"1 High St"}, Charset: "utf8",
		Voucher: VoucherBlock{Label: "Gift voucher", Amount: "10.00", Code: "ABC123"}}
}

func TestRenderVoucherSlip_NoStoreNameRow(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		unnamed := RenderVoucherSlip(omitSlip(blank))
		if bytes.Contains(unnamed, cmdDoubleOn) {
			t.Errorf("name %q: unnamed slip still has a double-width pair", blank)
		}
		named := RenderVoucherSlip(omitSlip(omitName))
		row := append(append(append([]byte{}, cmdDoubleOn...), []byte(omitName+"\n")...), cmdDoubleOff...)
		if got := bytes.Replace(named, row, nil, 1); !bytes.Equal(got, unnamed) {
			t.Errorf("name %q: named minus its row != unnamed", blank)
		}
	}
}

func TestRenderVoucherSlipText_NoStoreNameRow(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		unnamed := RenderVoucherSlipText(omitSlip(blank))
		if !strings.HasPrefix(unnamed, omitCentred("1 High St")) {
			t.Errorf("name %q: unnamed slip text should start with the header, got %q", blank, unnamed[:20])
		}
		named := RenderVoucherSlipText(omitSlip(omitName))
		if named != omitCentred(omitName)+unnamed {
			t.Errorf("name %q: named != name row + unnamed", blank)
		}
	}
}
