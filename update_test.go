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

// TURBOWEB_EXTENSION_DIR points at the extension SOURCE root (the dir with
// manifest.json holding dist/chrome and dist/firefox); the SAME override must
// resolve BOTH browsers' dist dirs so self_update can hot-swap either from an
// installed binary.
func TestExtensionDistDirs_EnvOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TURBOWEB_EXTENSION_DIR", root)

	// Env set but not a valid extension source root (no manifest) → both empty (strict).
	if got := findChromeExtensionDistDir(); got != "" {
		t.Errorf("no source manifest: chrome got %q, want empty", got)
	}
	if got := findFirefoxExtensionDistDir(); got != "" {
		t.Errorf("no source manifest: firefox got %q, want empty", got)
	}

	// Mark the env dir as an extension source root.
	if err := os.WriteFile(filepath.Join(root, "manifest.json"),
		[]byte(`{"version":"9.9.9"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Source root but no built dist dirs yet → still empty.
	if got := findChromeExtensionDistDir(); got != "" {
		t.Errorf("no dist/chrome: got %q, want empty", got)
	}

	// Build both dist dirs → the one override now resolves BOTH browsers.
	chromeDist := filepath.Join(root, "dist", "chrome")
	fxDist := filepath.Join(root, "dist", "firefox")
	for _, d := range []string{chromeDist, fxDist} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "manifest.json"),
			[]byte(`{"version":"1.2.3"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := findChromeExtensionDistDir(); got != chromeDist {
		t.Errorf("chrome: got %q, want %q", got, chromeDist)
	}
	if got := findFirefoxExtensionDistDir(); got != fxDist {
		t.Errorf("firefox: got %q, want %q", got, fxDist)
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

	if err := extractExtensionZip(srv.URL+"/extension.zip", dest, "chrome/"); err != nil {
		t.Fatalf("extractExtensionZip: %v", err)
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

	if err := extractExtensionZip(srv.URL, dest, "chrome/"); err != nil {
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

func TestExtractExtensionZip_FirefoxPrefix(t *testing.T) {
	dest := t.TempDir()

	// Firefox release zip carries a firefox/ prefix (zip -qr ... firefox).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"manifest.json": `{"version":"1.11.0"}`,
		"background.js":  `console.log("ff")`,
	} {
		w, _ := zw.Create("firefox/" + name)
		_, _ = w.Write([]byte(content))
	}
	zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	if err := extractExtensionZip(srv.URL, dest, "firefox/"); err != nil {
		t.Fatalf("extractExtensionZip(firefox): %v", err)
	}
	if v := extensionDirVersion(dest); v != "1.11.0" {
		t.Errorf("firefox manifest version after extraction: got %q, want 1.11.0", v)
	}
	if _, err := os.Stat(filepath.Join(dest, "background.js")); err != nil {
		t.Errorf("expected background.js with firefox/ prefix stripped: %v", err)
	}
}
