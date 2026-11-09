package pages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// The asked-options cache (ADR-0121 S5, ut-docs#4160): in memory, per plugin
// and per (version, setting key, current values). Only real answers are
// cached (an empty list included); a failure or no answer never is, so a new
// grant or a Try again works at once. A save of the plugin's settings drops
// its entries (dropPlugin), so a new API key refetches the model list.
const (
	settingsOptionsTTL          = 10 * time.Minute
	settingsOptionsMaxPerPlugin = 64
)

type cachedOptions struct {
	opts []askedOption
	at   time.Time
}

type settingsOptionsCache struct {
	mu       sync.Mutex
	now      func() time.Time
	byPlugin map[string]map[string]cachedOptions // pluginID -> cache key -> entry
}

func newSettingsOptionsCache(now func() time.Time) *settingsOptionsCache {
	return &settingsOptionsCache{now: now, byPlugin: map[string]map[string]cachedOptions{}}
}

// sharedSettingsOptionsCache is the process-wide cache. Only installed
// plugin ids reach put, which bounds the outer map.
var sharedSettingsOptionsCache = newSettingsOptionsCache(time.Now)

func (c *settingsOptionsCache) expired(e cachedOptions) bool {
	return !c.now().Before(e.at.Add(settingsOptionsTTL))
}

// get returns a copy of a live entry; an expired one is a miss.
func (c *settingsOptionsCache) get(pluginID, k string) ([]askedOption, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byPlugin[pluginID][k]
	if !ok {
		return nil, false
	}
	if c.expired(e) {
		delete(c.byPlugin[pluginID], k)
		return nil, false
	}
	return slices.Clone(e.opts), true
}

// put stores a copy, pruning expired entries first and evicting the oldest
// when the plugin is at its cap.
func (c *settingsOptionsCache) put(pluginID, k string, opts []askedOption) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.byPlugin[pluginID]
	if m == nil {
		m = map[string]cachedOptions{}
		c.byPlugin[pluginID] = m
	}
	for key, e := range m {
		if c.expired(e) {
			delete(m, key)
		}
	}
	if _, exists := m[k]; !exists && len(m) >= settingsOptionsMaxPerPlugin {
		oldest, first := "", true
		var oldestAt time.Time
		for key, e := range m {
			if first || e.at.Before(oldestAt) {
				oldest, oldestAt, first = key, e.at, false
			}
		}
		delete(m, oldest)
	}
	m[k] = cachedOptions{opts: slices.Clone(opts), at: c.now()}
}

// dropPlugin forgets every entry of one plugin.
func (c *settingsOptionsCache) dropPlugin(pluginID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byPlugin, pluginID)
}

// settingsOptionsCacheKey is version + setting key + a hash of the values
// (json.Marshal sorts map keys, so equal maps hash equal).
func settingsOptionsCacheKey(version, settingKey string, values map[string]string) string {
	b, _ := json.Marshal(values)
	sum := sha256.Sum256(b)
	return version + "\x00" + settingKey + "\x00" + hex.EncodeToString(sum[:])
}
