package pos

import "github.com/universaltill/universal-till/internal/money"

// updateLineByCode is the old code-addressed Service.UpdateLine, kept for
// tests only: the till addresses lines by key since ut-docs#5417, so the
// production method became dead code. It updates the first line matching
// code (SKU, item or variant id); no match is a no-op.
func updateLineByCode(s *Service, code string, qty float64, discount money.Money) error {
	for _, l := range s.Lines() {
		if l.SKU == code || l.ItemID == code || l.VariantID == code {
			return s.UpdateLineByKey(l.LineKey, qty, discount)
		}
	}
	return nil
}
