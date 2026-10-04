package print

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoDrawerPrinter is OpenDrawer's answer when no printer is configured
// (printer.mode off/unset): the cash drawer hangs off the receipt printer's
// kick connector, so with no printer there is nothing to kick.
var ErrNoDrawerPrinter = errors.New("print: no receipt printer configured to open the cash drawer")

// ErrDrawerUnsupported is OpenDrawer's answer for a printer type with no
// drawer-kick connector — a "system" (CUPS lp) office printer, which would
// print the ESC/POS kick bytes as garbage.
var ErrDrawerUnsupported = errors.New("print: the configured printer type cannot open a cash drawer")

// DrawerKickBytes is the ESC/POS drawer-kick pulse (ESC p m 25 250) for the
// configured connector pin: 5 selects pin 5, anything else pin 2 — the same
// mapping Render applies to Doc.DrawerPin (ut-docs#1136).
func DrawerKickBytes(pin int) []byte {
	src := cmdKickDrawer
	if pin == 5 {
		src = cmdKickDrawerPin5
	}
	return append([]byte(nil), src...)
}

// OpenDrawer kicks the cash drawer without printing anything (ut-docs#2558,
// "No sale"): only the kick pulse goes over the configured thermal
// transport — no init, no text, no feed, no cut. It returns
// ErrNoDrawerPrinter when printing is off, ErrDrawerUnsupported for a
// system printer, and the transport's error when the bytes could not be
// delivered; nil means the printer accepted the pulse.
func OpenDrawer(ctx context.Context, c Config) error {
	switch c.Mode {
	case "", "off":
		return ErrNoDrawerPrinter
	case "system":
		return ErrDrawerUnsupported
	}
	tr, err := NewTransport(c)
	if err != nil {
		return fmt.Errorf("open drawer: %w", err)
	}
	if tr == nil {
		return ErrNoDrawerPrinter
	}
	if err := tr.Print(ctx, DrawerKickBytes(c.DrawerPin)); err != nil {
		return fmt.Errorf("open drawer: %w", err)
	}
	return nil
}
