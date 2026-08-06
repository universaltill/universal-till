package pages

// csvSafe defuses CSV/formula injection: Excel/LibreOffice interpret a field
// starting with '=', '+', '-', '@', a tab, or a carriage return as a formula
// on open (e.g. `=cmd|'/c calc'!A1`). Prefixing a leading "'" is the standard
// mitigation — spreadsheet apps render the field as inert text (the leading
// quote itself isn't shown) instead of evaluating it. It's not invisible to
// every consumer, though: encoding/csv round-trips that "'" as a literal,
// ordinary rune (see TestCSVSafeRoundTripsThroughRealCSVWriter), so anything
// downstream comparing the raw field value — a reconciliation script, an
// exact-match import — sees it too.
//
// "-" alone is this codebase's own sentinel for "no entity ID"
// (InsertAudit's third arg at ~a dozen call sites), not attacker input —
// exempted so the audit export's most common Entity ID value isn't
// needlessly rewritten on every row (ut-docs#195 review).
func csvSafe(field string) string {
	if field == "" || field == "-" {
		return field
	}
	switch field[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + field
	default:
		return field
	}
}
