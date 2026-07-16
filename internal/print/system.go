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
	switch c.Mode {
	case "system":
		return systemPrint(ctx, c, RenderText(doc))
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
		return tr.Print(ctx, Render(doc))
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
