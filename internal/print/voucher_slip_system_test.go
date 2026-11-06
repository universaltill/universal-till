package print

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrintDocWithSlips_SystemModeFormFeeds: in system (lp) mode the slips
// follow the receipt in the same job, each after a form feed (ut-docs#4247).
// A fake `lp` on PATH captures stdin.
func TestPrintDocWithSlips_SystemModeFormFeeds(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "job.txt")
	script := "#!/bin/sh\ncat > \"$LP_CAPTURE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "lp"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LP_CAPTURE", out)

	d := sampleDoc()
	a, b := sampleSlip("GV-A", "For Anna"), sampleSlip("GV-B", "")
	if err := PrintDocWithSlips(context.Background(), Config{Mode: "system"}, d, []VoucherSlip{a, b}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := RenderText(d) + "\f" + RenderVoucherSlipText(a) + "\f" + RenderVoucherSlipText(b)
	if string(got) != want {
		t.Fatalf("system job mismatch:\n%q\nwant\n%q", got, want)
	}
	if n := strings.Count(string(got), "\f"); n != 2 {
		t.Fatalf("form feeds = %d, want 2", n)
	}
}
