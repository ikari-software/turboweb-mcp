package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestExtensionDirVersion(t *testing.T) {
	dir := t.TempDir()

	// missing manifest → empty string, no panic
	if v := extensionDirVersion(dir); v != "" {
		t.Errorf("missing manifest: got %q, want empty", v)
	}

	// valid manifest → version field returned
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"manifest_version":3,"name":"Test","version":"1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := extensionDirVersion(dir); v != "1.2.3" {
		t.Errorf("got %q, want 1.2.3", v)
	}

	// malformed manifest → empty string, no panic
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := extensionDirVersion(dir); v != "" {
		t.Errorf("malformed manifest: got %q, want empty", v)
	}
}

func TestFindChromeExtensionDistDir_EnvOverride(t *testing.T) {
	dir := t.TempDir()

	// Env var pointing at a non-extension directory → not found
	t.Setenv("TURBOWEB_EXTENSION_DIR", dir)
	if got := findChromeExtensionDistDir(); got != "" {
		t.Errorf("no manifest in dir: got %q, want empty", got)
	}

	// Env var pointing at a valid extension directory → returned as-is
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"version":"9.9.9"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findChromeExtensionDistDir(); got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

// makeTestChromeZip builds an in-memory zip that mirrors the structure
// produced by `make extension-zip`: files live under a chrome/ prefix.
func makeTestChromeZip(files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := zw.Create("chrome/" + name)
		_, _ = w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func TestExtractChromeZip(t *testing.T) {
	dest := t.TempDir()

	zipData := makeTestChromeZip(map[string]string{
		"manifest.json":  `{"version":"1.8.3"}`,
		"background.js":  `console.log("hello")`,
		"icons/icon.png": "PNG",
	})

	// Serve the zip over a local HTTP test server so we exercise the full
	// downloadTo → unzip path without hitting the network.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	if err := extractChromeZip(srv.URL+"/extension.zip", dest); err != nil {
		t.Fatalf("extractChromeZip: %v", err)
	}

	// Verify files were extracted with the chrome/ prefix stripped.
	for _, name := range []string{"manifest.json", "background.js", filepath.Join("icons", "icon.png")} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("expected %s to exist after extraction: %v", name, err)
		}
	}

	// Verify the extracted manifest has the right version.
	if v := extensionDirVersion(dest); v != "1.8.3" {
		t.Errorf("extracted manifest version: got %q, want 1.8.3", v)
	}
}

func TestExtractChromeZip_ZipSlipRejected(t *testing.T) {
	dest := t.TempDir()

	// A malicious zip with a path traversal entry should be silently skipped
	// (not extracted outside dest).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("chrome/../../evil.txt")
	_, _ = w.Write([]byte("pwned"))
	zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	if err := extractChromeZip(srv.URL, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The traversal entry must not have landed outside dest.
	parent := filepath.Dir(dest)
	if _, err := os.Stat(filepath.Join(parent, "evil.txt")); err == nil {
		t.Error("zip-slip: traversal file was extracted outside dest")
	}
}

func TestClampReleaseNotes(t *testing.T) {
	if got := clampReleaseNotes("  hello  "); got != "hello" {
		t.Errorf("expected trimmed %q, got %q", "hello", got)
	}
	if got := clampReleaseNotes(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
	long := strings.Repeat("x", maxReleaseNotes+500)
	got := clampReleaseNotes(long)
	if len(got) <= maxReleaseNotes {
		t.Errorf("over-length notes should keep the cap + suffix, got len %d", len(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("x", maxReleaseNotes)) {
		t.Error("clamped notes should preserve the first maxReleaseNotes chars")
	}
	if !strings.Contains(got, "truncated") {
		t.Error("clamped notes should flag truncation")
	}
}
