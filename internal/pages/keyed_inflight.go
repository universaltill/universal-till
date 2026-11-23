package pages

import "sync"

// keyedInFlight is a set of keys with an operation currently running
// (ut-docs#2714, #6909): acquire reports false when key is already in it.
type keyedInFlight struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func newKeyedInFlight() *keyedInFlight {
	return &keyedInFlight{keys: map[string]struct{}{}}
}

func (g *keyedInFlight) acquire(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, busy := g.keys[key]; busy {
		return false
	}
	g.keys[key] = struct{}{}
	return true
}

func (g *keyedInFlight) release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.keys, key)
}
