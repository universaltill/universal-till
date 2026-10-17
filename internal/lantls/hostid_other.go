//go:build !(linux && !android) && !windows && !(darwin && !ios)

package lantls

// readHostID knows no id here (Android, iOS: the app sandbox makes a copy
// of the data directory to other hardware unlikely), so it never rotates.
func readHostID() (source, raw string) { return "", "" }
