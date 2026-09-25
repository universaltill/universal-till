package cloudsync

// Test-only accessors for the satellite-skip log (kept out of the build so
// the whole-program deadcode baseline stays clean).

func resetSatelliteSkipLog() {
	satelliteSkipLog.mu.Lock()
	defer satelliteSkipLog.mu.Unlock()
	satelliteSkipLog.seen = nil
}

func satelliteSkipLoggedLen() int {
	satelliteSkipLog.mu.Lock()
	defer satelliteSkipLog.mu.Unlock()
	return len(satelliteSkipLog.seen)
}
