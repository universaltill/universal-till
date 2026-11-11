package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Bounds of the cloud heartbeat's quick_buttons report (ut-docs#2562).
// MaxReportedQuickButtons equals ut-cloud's maxQuickButtonBarcodes (the
// set_quick_button_layout cap), so a reported list always fits one directive.
const (
	MaxReportedQuickButtons          = 500
	MaxReportedQuickButtonCodeBytes  = 64  // ut-cloud directive barcode cap
	MaxReportedQuickButtonLabelRunes = 200 // ut-cloud maxReportedNameRunes
)

// ErrLayoutGridDegraded is ApplyReportedLayout's refusal when a lookup in
// the grid build fell back: a degraded SellScreenStates read marks hidden
// items visible, so validating against it could lay out a hidden tile.
var ErrLayoutGridDegraded = errors.New("this till could not read its whole sale screen just now; try the layout again after its next check-in")

// QuickButtonsReportVersion is the change marker of what the cloud report is
// built from (ut-docs#2562 D1): the catalog-only sell_screen_version
// generation (migrations 042/047/065 bump it on items, barcodes, shortcut
// rows, categories, ...; never on settings, so the per-check-in settings
// rewrite of the entitlement cache cannot move it) plus the enabled barcode
// symbologies, the one settings input of a tile's code (loadAllActive).
// Not sync_admin_version: the settings trigger bumps that on every check-in.
type QuickButtonsReportVersion struct {
	Sell        int64
	Symbologies string
}

// QuickButtonsReportVersion reads the marker; ok=false (no row, no repo, or
// a failed symbology read) means "don't suppress".
func (s *ButtonStore) QuickButtonsReportVersion(ctx context.Context) (QuickButtonsReportVersion, bool, error) {
	if s.sellRepo == nil || s.settingsRepo == nil {
		return QuickButtonsReportVersion{}, false, nil
	}
	gen, ok, err := s.sellRepo.SellGeneration(ctx)
	if err != nil || !ok {
		return QuickButtonsReportVersion{}, false, err
	}
	ids, err := s.settingsRepo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		return QuickButtonsReportVersion{}, false, err
	}
	return QuickButtonsReportVersion{Sell: gen, Symbologies: strings.Join(ids, ",")}, true, nil
}

// LoadGrid is Load()'s grid (explicit rows incl. hidden, then implicit tiles
// by name incl. hidden; removed items absent), but strict: a loadAllActive
// error is returned (Load() swallows it and falls back to explicit rows),
// and degraded reports whether any lookup fell back (see loadAllActive).
func (s *ButtonStore) LoadGrid(ctx context.Context) (grid []Button, degraded bool, err error) {
	allActive, d1, err := s.loadAllActive(ctx)
	if err != nil {
		return nil, false, err
	}
	out, d2, err := s.loadWith(ctx, allActive)
	if err != nil {
		return nil, false, err
	}
	return out, d1 || d2, nil
}

// ReportableTiles is the pure core both the cloud report and the layout
// directive use, so they can never disagree on the set:
//   - head: the first MaxReportedQuickButtons tiles of grid that are
//     !Hidden, have a non-empty code of <= MaxReportedQuickButtonCodeBytes,
//     and whose code was not already taken by an earlier tile (first
//     occurrence wins, hidden tiles included in that "earlier" check);
//   - total: the number of !Hidden tiles in grid (each counted once,
//     whatever its code).
func ReportableTiles(grid []Button) (head []Button, total int) {
	taken := make(map[string]bool, len(grid))
	for _, b := range grid {
		first := !taken[b.Code]
		taken[b.Code] = true
		if b.Hidden {
			continue
		}
		total++
		if !first || b.Code == "" || len(b.Code) > MaxReportedQuickButtonCodeBytes || len(head) >= MaxReportedQuickButtons {
			continue
		}
		head = append(head, b)
	}
	return head, total
}

// ReportLabel cuts a label to MaxReportedQuickButtonLabelRunes runes (whole
// runes, never mid-UTF-8).
func ReportLabel(s string) string {
	if len(s) <= MaxReportedQuickButtonLabelRunes {
		return s
	}
	r := []rune(s)
	if len(r) <= MaxReportedQuickButtonLabelRunes {
		return s
	}
	return string(r[:MaxReportedQuickButtonLabelRunes])
}

// ApplyReportedLayout is set_quick_button_layout's core (ut-docs#2562; #2709
// hidden slots). codes must be exactly the ReportableTiles head's code set,
// in the new order. The codes are dealt into the slots the head occupies;
// every other tile (hidden, long-code, shared second occurrences, past the
// cap) keeps its slot. Implicit tiles at or before the last moved position
// are materialised in the same transaction. Nothing is written on a refusal.
// Returns the number of codes laid out.
func (s *ButtonStore) ApplyReportedLayout(ctx context.Context, codes []string) (int, error) {
	grid, degraded, err := s.LoadGrid(ctx)
	if err != nil {
		return 0, err
	}
	if degraded {
		return 0, ErrLayoutGridDegraded
	}
	head, _ := ReportableTiles(grid)
	headSet := make(map[string]bool, len(head))
	for _, b := range head {
		headSet[b.Code] = true
	}
	hiddenSet := make(map[string]bool)
	count := make(map[string]int, len(grid))
	for _, b := range grid {
		count[b.Code]++
		if b.Hidden {
			hiddenSet[b.Code] = true
		}
	}
	given := make(map[string]bool, len(codes))
	for _, c := range codes {
		if given[c] {
			return 0, fmt.Errorf("duplicate barcode %q in the new layout", c)
		}
		given[c] = true
		if hiddenSet[c] && !headSet[c] {
			return 0, fmt.Errorf("barcode %q is hidden on this till's sell screen — the layout lists visible quick buttons only", c)
		}
		if !headSet[c] {
			return 0, fmt.Errorf("barcode %q is not one of this till's quick buttons", c)
		}
	}
	for _, b := range head {
		if !given[b.Code] {
			return 0, fmt.Errorf("the new layout is missing barcode %q — it must list every current quick button", b.Code)
		}
	}
	full := redealIntoSlots(grid, codes)
	last := -1
	for i, c := range full {
		if c != grid[i].Code {
			last = i
		}
	}
	if last < 0 {
		return len(codes), nil
	}
	for i := 0; i <= last; i++ {
		if count[full[i]] > 1 {
			return 0, sharedLayoutCodeError{code: full[i]}
		}
	}
	if err := s.updateOrderWith(ctx, grid, full); err != nil {
		return 0, err
	}
	return len(codes), nil
}

// sharedLayoutCodeError is ApplyReportedLayout's owner-readable refusal for a
// code held by two tiles; errors.Is matches ErrSharedTileCode.
type sharedLayoutCodeError struct{ code string }

func (e sharedLayoutCodeError) Error() string {
	return fmt.Sprintf("barcode %q is used by two sale-screen tiles on this till — give the items distinct SKUs or barcodes first", e.code)
}

func (e sharedLayoutCodeError) Is(target error) bool { return target == ErrSharedTileCode }
