package print

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PrintDoc renders a document for the configured printer TYPE and delivers it:
//   - "system": a regular desktop/office printer (e.g. an HP) — send a plain-
//     TEXT layout to the OS print system (CUPS `lp`). ESC/POS control bytes on
//     such a printer come out as garbage / one line each, which is why a
//     thermal-only path isn't enough.
//   - "network"/"device": a thermal ESC/POS printer — send the ESC/POS stream.
//   - "off"/"": nothing.
//
// This is the single entry point callers should use so every receipt/report
// respects the printer type.
func PrintDoc(ctx context.Context, c Config, doc Doc) error {
	return PrintDocWithSlips(ctx, c, doc, nil)
}

// PrintDocWithSlips prints doc followed by each voucher slip (ut-docs#4247)
// as ONE job: the slips can't be split from their receipt by another print,
// and the result is one success or one failure. With nil slips the output is
// byte-identical to PrintDoc.
func PrintDocWithSlips(ctx context.Context, c Config, doc Doc, slips []VoucherSlip) error {
	switch c.Mode {
	case "system":
		text := RenderText(doc)
		for _, s := range slips {
			text += "\f" + RenderVoucherSlipText(s)
		}
		return systemPrint(ctx, c, text)
	case "off", "":
		return nil
	default:
		tr, err := NewTransport(c)
		if err != nil {
			return err
		}
		if tr == nil {
			return nil
		}
		job := Render(doc)
		for _, s := range slips {
			job = append(job, RenderVoucherSlip(s)...)
		}
		return tr.Print(ctx, job)
	}
}

// PrintVoucherSlip prints ONE voucher slip on its own — no receipt — as one
// job (ut-docs#5740): ESC/POS for a thermal printer (one feed+cut, no drawer
// kick), the plain-text slip via lp for a system printer, nothing when off.
func PrintVoucherSlip(ctx context.Context, c Config, s VoucherSlip) error {
	switch c.Mode {
	case "system":
		return systemPrint(ctx, c, RenderVoucherSlipText(s))
	case "off", "":
		return nil
	default:
		tr, err := NewTransport(c)
		if err != nil {
			return err
		}
		if tr == nil {
			return nil
		}
		return tr.Print(ctx, RenderVoucherSlip(s))
	}
}

// systemPrint sends plain text to a regular printer via CUPS `lp`. The printer
// name (optional) is taken from Address; empty uses the system default printer.
// A monospaced pitch keeps the receipt's column alignment.
func systemPrint(ctx context.Context, c Config, text string) error {
	args := []string{"-t", "Universal Till", "-o", "cpi=12", "-o", "lpi=8"}
	if name := strings.TrimSpace(c.Address); name != "" {
		args = append(args, "-d", name)
	}
	cmd := exec.CommandContext(ctx, "lp", args...)
	cmd.Stdin = bytes.NewReader([]byte(text))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lp print failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
