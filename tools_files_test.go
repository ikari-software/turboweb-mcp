package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHostPath_RejectsRelative(t *testing.T) {
	_, _, err := resolveHostPath("relative/path.txt", "local_file_path")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error %q should mention 'absolute'", err)
	}
}

func TestResolveHostPath_RejectsEmpty(t *testing.T) {
	_, _, err := resolveHostPath("", "local_file_path")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestResolveHostPath_RejectsMissing(t *testing.T) {
	_, _, err := resolveHostPath("/nonexistent/turboweb/missing.file", "local_file_path")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestResolveHostPath_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveHostPath(dir, "local_file_path")
	if err == nil {
		t.Fatal("expected error for directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error %q should mention 'directory'", err)
	}
}

func TestResolveHostPath_AcceptsRegularFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ok.txt")
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	resolved, info, err := resolveHostPath(p, "local_file_path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Size() != 4 {
		t.Errorf("size = %d, want 4", info.Size())
	}
	if !filepath.IsAbs(resolved) {
		t.Errorf("resolved path %q is not absolute", resolved)
	}
}

func TestResolveHostPath_ResolvesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	resolved, _, err := resolveHostPath(link, "local_file_path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// EvalSymlinks must collapse the link to its target.
	realResolved, _ := filepath.EvalSymlinks(real)
	if resolved != realResolved {
		t.Errorf("resolved = %q, want symlink target %q", resolved, realResolved)
	}
}

func TestSniffMimeType(t *testing.T) {
	tests := []struct {
		name     string
		override string
		path     string
		want     string // substring expected in result
	}{
		{"override wins", "application/x-custom", "/tmp/file.txt", "application/x-custom"},
		{"pdf by extension", "", "/tmp/report.pdf", "application/pdf"},
		{"png by extension", "", "/tmp/img.png", "image/png"},
		{"unknown extension falls back", "", "/tmp/blob.zzzqqq", "application/octet-stream"},
		{"no extension falls back", "", "/tmp/noext", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sniffMimeType(tt.override, tt.path)
			if !strings.Contains(got, tt.want) {
				t.Errorf("sniffMimeType(%q, %q) = %q, want to contain %q",
					tt.override, tt.path, got, tt.want)
			}
		})
	}
}
