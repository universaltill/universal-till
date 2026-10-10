//go:build race

package data

// raceEnabled: the race detector slows modernc's transpiled SQLite by
// ~40×, so wall-clock budgets (ut-docs#3280) widen under -race.
const raceEnabled = true
