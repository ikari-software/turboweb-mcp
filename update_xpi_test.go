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
		{Browser: "Chrome", Version: "1.0.0", InstallType: "normal"}, // not Firefox family
		{Browser: "Zen"}, // pre-1.18: no version, behind
		{Browser: "Zen", Version: "1.0.0", InstallType: "normal"},          // second profile, one prompt
		{Browser: "Firefox", Version: "1.0.0", InstallType: "development"}, // temporary add-on: swapped on disk
	}, "1.18.0", false)
	want := []xpiTarget{{Browser: "Zen"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := staleXPIBrowsers([]extensionInfo{{Browser: "Firefox", Version: "1.18.0", InstallType: "normal"}}, "1.18.0", false); len(got) != 0 {
		t.Errorf("a current install needs nothing, got %+v", got)
	}
	if got := staleXPIBrowsers([]extensionInfo{{Browser: "Firefox", Version: "1.17.0", InstallType: "normal"}}, "1.18.0", false); len(got) != 1 {
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
	fetchExtensionInfo = func() ([]extensionInfo, bool) { return exts, exts != nil }
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

	prompted, note := installStaleXPIs(st, false)
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

	prompted, note := installStaleXPIs(st, false)
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
	if prompted, note := installStaleXPIs(st, false); prompted != nil || note != "" || len(*opened) != 0 {
		t.Errorf("prompted=%v note=%q opened=%v", prompted, note, *opened)
	}
}

func TestStaleXPIBrowsers_SkipsUnreportedAfterDistSwap(t *testing.T) {
	exts := []extensionInfo{{Browser: "Firefox"}, {Browser: "Zen", Version: "1.17.0", InstallType: "normal"}}
	got := staleXPIBrowsers(exts, "1.18.0", true)
	if want := []xpiTarget{{Browser: "Zen", Version: "1.17.0"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("after a dist swap an extension without installType may be the swapped temporary add-on: got %+v, want %+v", got, want)
	}
	if got := staleXPIBrowsers(exts, "1.18.0", false); len(got) != 2 {
		t.Errorf("without a swap both are behind, got %+v", got)
	}
}

func TestInstallStaleXPIs_SaysWhenItCouldNotCheck(t *testing.T) {
	st := xpiRelease(t, "x", strings.Repeat("0", 64))
	_, opened := withXPISeams(t, nil) // no answer from the daemon
	prompted, note := installStaleXPIs(st, false)
	if prompted != nil || len(*opened) != 0 || !strings.Contains(note, "could not read which browser extensions") {
		t.Errorf("prompted=%v opened=%v note=%q", prompted, *opened, note)
	}
	st.xpiURL = ""
	if _, note := installStaleXPIs(st, false); !strings.Contains(note, "has no Firefox .xpi") {
		t.Errorf("a release without an .xpi should say so, got %q", note)
	}
}

func TestInstallStaleXPIs_ReportsPathWhenOpenFails(t *testing.T) {
	body := "PK"
	h := sha256.Sum256([]byte(body))
	st := xpiRelease(t, body, hex.EncodeToString(h[:]))
	dir, _ := withXPISeams(t, []extensionInfo{{Browser: "Zen"}})
	openXPI = func(string, string) error { return fmt.Errorf("no such app") }
	prompted, note := installStaleXPIs(st, false)
	if len(prompted) != 0 || !strings.Contains(note, filepath.Join(dir, st.xpiName)) || !strings.Contains(note, "no such app") {
		t.Errorf("prompted=%v note=%q", prompted, note)
	}
}

func TestOfferXPI_LeadsWithTheUsersAction(t *testing.T) {
	body := "PK"
	h := sha256.Sum256([]byte(body))
	st := xpiRelease(t, body, hex.EncodeToString(h[:]))
	withXPISeams(t, []extensionInfo{{Browser: "Zen"}})
	res := updateResult{Message: "already on the latest version (1.18.0)"}
	offerXPI(&res, st, false)
	if !res.UserActionRequired || !strings.HasPrefix(res.Message, "ACTION NEEDED: click Add in Zen") {
		t.Errorf("res = %+v", res)
	}
}
