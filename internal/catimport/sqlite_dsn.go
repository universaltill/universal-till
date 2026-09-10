package catimport

import "strings"

// sqliteURIPathEscaper percent-encodes the three characters significant to
// SQLite's own "file:" URI filename parsing (ut-docs#2033, mirroring
// ut-docs#2030's internal/db.escapeSQLiteURIPath): '#', '?' and '%' itself.
// ParseBkp's temp backup.db copy is opened via sql.Open("sqlite",
// fmt.Sprintf("file:%s?...", tmpPath)) below — an unescaped '#' or '?' in
// tmpPath (which inherits its containing directory from os.CreateTemp, so
// exposure depends on TMPDIR's own naming) silently truncates the path AND
// drops the trailing _pragma query string, with no error.
//
// This is a local copy of internal/db's own helper rather than an import
// of it: at the time this fix was written, internal/db's version
// (ut-docs#2030) was still in an open, unmerged PR and unexported besides.
// A follow-up should consolidate the two into one exported helper once
// that PR lands — tracked as part of ut-docs#2033's own remaining scope,
// not deferred silently.
var sqliteURIPathEscaper = strings.NewReplacer("%", "%25", "#", "%23", "?", "%3f")

func escapeSQLiteURIPath(path string) string {
	return sqliteURIPathEscaper.Replace(path)
}
