package print

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// slip6179Variants are the three slips whose bytes the ut-docs#6179 change
// must leave untouched while Date and Text are empty: an issued slip, a
// top-up slip and a slip with a Note (voucher-page reprint).
func slip6179Variants() map[string]VoucherSlip {
	issued := sampleSlip("GV-7K3P-Q2LM", "For Anna")
	topup := sampleSlip("GV-7K3P-Q2LM", "For Anna")
	topup.Voucher.Label = "Gift voucher top-up"
	topup.Voucher.Amount = "+10.00"
	topup.Voucher.Balance = "New balance 35.00"
	note := sampleSlip("GV-7K3P-Q2LM", "")
	note.Note = "Reprinted 06/11/2026"
	return map[string]VoucherSlip{"issued": issued, "topup": topup, "note": note}
}

// TestRenderVoucherSlip_EmptyDateAndTextByteIdentical pins the pre-#6179 output
// (testdata/voucher_slip_6179_*) so an empty Date and Text add no byte.
func TestRenderVoucherSlip_EmptyDateAndTextByteIdentical(t *testing.T) {
	for name, s := range slip6179Variants() {
		base := filepath.Join("testdata", "voucher_slip_6179_"+name)
		wantE, err := os.ReadFile(base + ".escpos")
		if err != nil {
			t.Fatal(err)
		}
		if got := RenderVoucherSlip(s); !bytes.Equal(got, wantE) {
			t.Errorf("%s: ESC/POS bytes changed", name)
		}
		wantT, err := os.ReadFile(base + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		if got := RenderVoucherSlipText(s); got != string(wantT) {
			t.Errorf("%s: text output changed:\n%s", name, got)
		}
	}
}
