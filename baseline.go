package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// baseline.go — the opaque-token cache behind screenshot_diff's
// capture-bracketed mode. A baseline-minting call stores the current
// capture (plus its DOM-mutation mark) under a short random id and hands
// that id back; the later verify call looks it up by id instead of having
// the agent re-upload the image. See design/screenshot_diff.md §3 / §6.
//
// The cache lives in the process that runs the tool handler (the relay/MCP
// process) — never in the extension service worker, which gets evicted.
// It is a bounded, TTL'd LRU: at most baselineCacheCap entries, each valid
// for baselineCacheTTL.

const (
	// baselineCacheCap bounds the cache; the oldest entry is evicted past it.
	baselineCacheCap = 16
	// baselineCacheTTL expires a baseline so a stale id surfaces a clear
	// "mint a new baseline" error rather than silently diffing rot.
	baselineCacheTTL = 5 * time.Minute
)

// baselineEntry is one cached capture: the image bytes, the DOM-mutation
// mark token taken at the same instant, the source URL (for the navigation
// short-circuit), and the mint time for TTL/LRU bookkeeping.
type baselineEntry struct {
	image   []byte
	domMark string
	url     string
	created time.Time
}

// baselineCache is a small mutex-guarded TTL LRU keyed by opaque hex id.
type baselineCache struct {
	mu      sync.Mutex
	entries map[string]baselineEntry
	// order holds ids oldest-first so eviction is O(1) amortized.
	order []string
	// now is overridable so tests can drive TTL/expiry deterministically.
	now func() time.Time
}

// theBaselineCache is the process-wide instance used by handleScreenshotDiff.
var theBaselineCache = newBaselineCache()

// newBaselineCache builds an empty cache with the real clock.
func newBaselineCache() *baselineCache {
	return &baselineCache{
		entries: make(map[string]baselineEntry),
		now:     time.Now,
	}
}

// put stores an entry under a fresh random id and returns that id. It also
// prunes expired entries and evicts the oldest if the cache is over cap.
func (c *baselineCache) put(e baselineEntry) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e.created.IsZero() {
		e.created = c.now()
	}
	c.pruneExpiredLocked()

	id := newBaselineID()
	c.entries[id] = e
	c.order = append(c.order, id)

	// Evict oldest entries while over capacity.
	for len(c.order) > baselineCacheCap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	return id
}

// get returns the entry for id. ok is false when the id is unknown or has
// expired — the handler turns that into an explicit error telling the agent
// to mint a new baseline (§7 expired-baseline case).
func (c *baselineCache) get(id string) (baselineEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pruneExpiredLocked()
	e, ok := c.entries[id]
	return e, ok
}

// pruneExpiredLocked drops entries older than the TTL. Caller holds c.mu.
func (c *baselineCache) pruneExpiredLocked() {
	cutoff := c.now().Add(-baselineCacheTTL)
	kept := c.order[:0]
	for _, id := range c.order {
		if e, ok := c.entries[id]; ok {
			if e.created.Before(cutoff) {
				delete(c.entries, id)
				continue
			}
			kept = append(kept, id)
		}
	}
	c.order = kept
}

// len reports the live (non-expired) entry count — used by tests.
func (c *baselineCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneExpiredLocked()
	return len(c.entries)
}

// newBaselineID returns a short random hex token, e.g. "b_8f3a1c". The
// "b_" prefix mirrors the design doc's examples and makes the id
// recognizable in logs without leaking anything.
func newBaselineID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is near-impossible; fall back to a
		// time-derived id rather than panicking inside a tool call.
		return "b_" + hex.EncodeToString([]byte(time.Now().Format("150405")))
	}
	return "b_" + hex.EncodeToString(b[:])
}
