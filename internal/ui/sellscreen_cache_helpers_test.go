package ui

// Len and Bytes report the cache's current occupancy. Test-only: production
// never reads them (guard-deadcode-baseline.sh).
func (c *SellScreenCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

func (c *SellScreenCache) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}
