package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStaleXPIBrowsers(t *testing.T) {
	got := staleXPIBrowsers([]extensionInfo{
		{Browser: "Chrome", Version: "1.0.0", InstallType: "normal"},       // not Firefox family
		{Browser: "Zen"},                                                   // pre-1.18: no version, behind
		{Browser: "Zen", Version: "1.0.0", InstallType: "normal"},          // second profile, one prompt
		{Browser: "Firefox", Version: "1.0.0", InstallType: "development"}, // temporary add-on: swapped on disk
	}, "1.18.0")
	want := []xpiTarget{{Browser: "Zen"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := staleXPIBrowsers([]extensionInfo{{Browser: "Firefox", Version: "1.18.0", InstallType: "normal"}}, "1.18.0"); len(got) != 0 {
		t.Errorf("a current install needs nothing, got %+v", got)
	}
	if got := staleXPIBrowsers([]extensionInfo{{Browser: "Firefox", Version: "1.17.0", InstallType: "normal"}}, "1.18.0"); len(got) != 1 {
		t.Errorf("an older .xpi install should be offered the release, got %+v", got)
	}
}

// xpiRelease serves an .xpi and a SHA256SUMS that lists sum for it.
func xpiRelease(t *testing.T, body, sum string) *updateStatus {
	t.Helper()
	name := xpiAssetPrefix + "1.18.0.xpi"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xpi":
			fmt.Fprint(w, body)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", sum, name)
		}
	}))
	t.Cleanup(srv.Close)
	return &updateStatus{LatestVersion: "1.18.0", xpiURL: srv.URL + "/xpi", xpiName: name, sumsURL: srv.URL + "/sums"}
}

func withXPISeams(t *testing.T, exts []extensionInfo) (dir string, opened *[]string) {
	t.Helper()
	dir = t.TempDir()
	opened = &[]string{}
	oldFetch, oldOpen, oldDir := fetchExtensionInfo, openXPI, xpiCacheDir
	fetchExtensionInfo = func() []extensionInfo { return exts }
	openXPI = func(browser, path string) error {
		*opened = append(*opened, browser+" "+filepath.Base(path))
		return nil
	}
	xpiCacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { fetchExtensionInfo, openXPI, xpiCacheDir = oldFetch, oldOpen, oldDir })
	return dir, opened
}

func TestInstallStaleXPIs_OpensVerifiedFile(t *testing.T) {
	body := "PK fake xpi"
	h := sha256.Sum256([]byte(body))
	st := xpiRelease(t, body, hex.EncodeToString(h[:]))
	dir, opened := withXPISeams(t, []extensionInfo{{Browser: "Zen", Version: "1.17.0", InstallType: "normal"}})

	prompted, note := installStaleXPIs(st)
	if !reflect.DeepEqual(prompted, []string{"Zen"}) || !strings.Contains(note, "click Add") {
		t.Fatalf("prompted=%v note=%q", prompted, note)
	}
	if want := []string{"Zen " + st.xpiName}; !reflect.DeepEqual(*opened, want) {
		t.Errorf("opened %v, want %v", *opened, want)
	}
	if b, err := os.ReadFile(filepath.Join(dir, st.xpiName)); err != nil || string(b) != body {
		t.Errorf("staged xpi = %q, %v", b, err)
	}
}

func TestInstallStaleXPIs_RefusesChecksumMismatch(t *testing.T) {
	st := xpiRelease(t, "tampered", strings.Repeat("0", 64))
	dir, opened := withXPISeams(t, []extensionInfo{{Browser: "Zen"}})

	prompted, note := installStaleXPIs(st)
	if len(prompted) != 0 || len(*opened) != 0 || !strings.Contains(note, "checksum mismatch") {
		t.Fatalf("prompted=%v opened=%v note=%q", prompted, *opened, note)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("nothing should be left behind, found %d files", len(entries))
	}
}

func TestInstallStaleXPIs_NothingToDo(t *testing.T) {
	st := xpiRelease(t, "x", strings.Repeat("0", 64))
	_, opened := withXPISeams(t, []extensionInfo{{Browser: "Zen", Version: "1.18.0", InstallType: "normal"}})
	if prompted, note := installStaleXPIs(st); prompted != nil || note != "" || len(*opened) != 0 {
		t.Errorf("prompted=%v note=%q opened=%v", prompted, note, *opened)
	}
}
