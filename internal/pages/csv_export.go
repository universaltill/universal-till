package pages

// csvSafe defuses CSV/formula injection: Excel/LibreOffice interpret a field
// starting with '=', '+', '-', '@', a tab, or a carriage return as a formula
// on open (e.g. `=cmd|'/c calc'!A1`). Prefixing a leading "'" is the standard
// mitigation — spreadsheet apps render the field as inert text (the leading
// quote itself isn't shown) instead of evaluating it, and the change is
// invisible to any consumer reading the CSV as plain data (that leading "'"
// is a spreadsheet-app-only formula-inhibiting convention, not a CSV syntax
// character, so encoding/csv round-trips it as a literal, ordinary rune).
func csvSafe(field string) string {
	if field == "" {
		return field
	}
	switch field[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + field
	default:
		return field
	}
}
