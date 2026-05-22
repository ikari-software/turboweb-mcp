package main

import (
	"testing"
	"time"
)

// baseline_test.go — coverage for the screenshot_diff opaque-id cache:
// mint → lookup → TTL expiry → LRU eviction → expired-id error path (§8).

func TestBaselineCache_PutGet(t *testing.T) {
	c := newBaselineCache()
	id := c.put(baselineEntry{image: []byte("img-a"), url: "https://example.com", domMark: "m1"})
	if id == "" {
		t.Fatal("put returned empty id")
	}
	entry, ok := c.get(id)
	if !ok {
		t.Fatalf("get(%q) miss right after put", id)
	}
	if string(entry.image) != "img-a" || entry.url != "https://example.com" || entry.domMark != "m1" {
		t.Errorf("round-tripped entry = %+v, want img-a/example.com/m1", entry)
	}
}

func TestBaselineCache_UnknownID(t *testing.T) {
	c := newBaselineCache()
	if _, ok := c.get("b_deadbeef"); ok {
		t.Error("get on unknown id returned ok=true")
	}
}

func TestBaselineCache_TTLExpiry(t *testing.T) {
	c := newBaselineCache()
	// Drive a virtual clock so expiry is deterministic.
	now := time.Unix(1_000_000, 0)
	c.now = func() time.Time { return now }

	id := c.put(baselineEntry{image: []byte("x")})
	if _, ok := c.get(id); !ok {
		t.Fatal("entry missing immediately after put")
	}

	// Advance past the TTL — the entry must be gone.
	now = now.Add(baselineCacheTTL + time.Second)
	if _, ok := c.get(id); ok {
		t.Errorf("entry survived past TTL %s", baselineCacheTTL)
	}
	if c.len() != 0 {
		t.Errorf("expired entry not pruned: len = %d", c.len())
	}
}

func TestBaselineCache_LRUEviction(t *testing.T) {
	c := newBaselineCache()
	ids := make([]string, 0, baselineCacheCap+4)
	// Insert four more than the cap.
	for i := 0; i < baselineCacheCap+4; i++ {
		ids = append(ids, c.put(baselineEntry{image: []byte{byte(i)}}))
	}
	if c.len() != baselineCacheCap {
		t.Fatalf("cache len = %d, want cap %d", c.len(), baselineCacheCap)
	}
	// The four oldest ids must have been evicted.
	for i := 0; i < 4; i++ {
		if _, ok := c.get(ids[i]); ok {
			t.Errorf("oldest id %q (#%d) should have been evicted", ids[i], i)
		}
	}
	// The most-recent id must still be present.
	if _, ok := c.get(ids[len(ids)-1]); !ok {
		t.Errorf("newest id %q evicted unexpectedly", ids[len(ids)-1])
	}
}

func TestBaselineCache_IDsUnique(t *testing.T) {
	c := newBaselineCache()
	seen := map[string]bool{}
	for i := 0; i < baselineCacheCap; i++ {
		id := c.put(baselineEntry{image: []byte{byte(i)}})
		if seen[id] {
			t.Fatalf("duplicate baseline id minted: %q", id)
		}
		seen[id] = true
	}
}
