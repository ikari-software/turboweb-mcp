package main

import (
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.3.0", "1.3.0", 0},
		{"1.4.0", "1.3.0", 1},
		{"1.3.0", "1.4.0", -1},
		{"v1.4.0", "1.3.0", 1},   // leading "v" ignored
		{"1.3.0", "v1.3.0", 0},   // ...on either side
		{"1.3", "1.3.0", 0},      // missing trailing component == 0
		{"1.3.1", "1.3", 1},      // ...and counts when present
		{"2.0.0", "1.9.9", 1},    // major beats minor/patch
		{"1.10.0", "1.9.0", 1},   // numeric, not lexical, ordering
		{" 1.3.0 ", "1.3.0", 0},  // surrounding whitespace trimmed
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestReleaseAssetName(t *testing.T) {
	// On a supported build platform the name must be non-empty; on an
	// unsupported one it must be "" so self-update fails loudly rather
	// than downloading a binary for the wrong architecture.
	name := releaseAssetName()
	if name != "" && !strings.HasPrefix(name, "turboweb-mcp-by-ikari-") {
		t.Errorf("releaseAssetName() = %q, want a turboweb-mcp-by-ikari-* asset or empty", name)
	}
}
