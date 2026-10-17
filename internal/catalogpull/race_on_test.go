//go:build race

package catalogpull

// raceEnabled: the race detector slows modernc's transpiled SQLite ~40×,
// so the 20 000-row upload (minutes under -race) runs only without it.
const raceEnabled = true
