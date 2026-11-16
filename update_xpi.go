package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Firefox-family browsers that run the extension from an installed .xpi do
// update themselves through the manifest's update_url, but not always: a
// build without MOZ_REQUIRE_SIGNING (Zen) checks add-on downloads against
// Mozilla's built-in roots only, so a TLS-filtering proxy (AdGuard and the
// like) makes every update check and GitHub install fail. self_update covers
// that gap: it downloads the release .xpi itself, verifies it against the
// signed SHA256SUMS, and hands it to the browser, which asks the user to Add it.

const xpiAssetPrefix = "turboweb-mcp-by-ikari-extension-firefox-"

// xpiTarget is a connected browser whose installed .xpi is behind the release.
type xpiTarget struct {
	Browser string // "Zen", "Firefox": the app name `open -a` takes on macOS
	Version string // installed version, "" when the extension is too old to say
}

// staleXPIBrowsers picks the browsers that need the release .xpi: Firefox
// family, not a temporary add-on (those are swapped on disk), and behind
// latest. An extension that reports no version predates 1.18, so it is behind.
func staleXPIBrowsers(exts []extensionInfo, latest string) []xpiTarget {
	var out []xpiTarget
	seen := map[string]bool{}
	for _, e := range exts {
		if e.Browser != "Zen" && e.Browser != "Firefox" {
			continue
		}
		if e.InstallType == "development" {
			continue
		}
		if e.Version != "" && compareVersions(latest, e.Version) <= 0 {
			continue
		}
		if seen[e.Browser] {
			continue // two profiles of one browser: one prompt installs for the one in front
		}
		seen[e.Browser] = true
		out = append(out, xpiTarget{Browser: e.Browser, Version: e.Version})
	}
	return out
}

// Seams for tests.
var (
	fetchExtensionInfo = connectedExtensions
	openXPI            = openXPIInBrowser
	xpiCacheDir        = func() (string, error) {
		d, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(d, "turboweb-mcp"), nil
	}
)

// connectedExtensions asks the process holding the browser connections (the
// daemon, via the relay, or this process) which extensions are connected. A
// daemon that self_update just replaced respawns on this call and the
// browsers reconnect within seconds, so an empty answer is retried briefly.
func connectedExtensions() []extensionInfo {
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw, err := send("connection_status", nil, 5000)
		if err == nil {
			var st struct {
				Extensions []extensionInfo `json:"extensions"`
			}
			if json.Unmarshal(raw, &st) == nil && len(st.Extensions) > 0 {
				return st.Extensions
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(time.Second)
	}
}

// openXPIInBrowser hands a verified .xpi to the browser, which shows its
// install prompt. Only macOS can name the app to open it with.
func openXPIInBrowser(browser, path string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("opening it automatically is only supported on macOS")
	}
	out, err := exec.Command("open", "-a", browser, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("open -a %s: %v: %s", browser, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// installStaleXPIs downloads and verifies the release .xpi and opens it in
// every connected browser whose installed copy is behind. It returns the
// browsers it prompted and a note for the result message ("" if nothing to do).
func installStaleXPIs(st *updateStatus) (prompted []string, note string) {
	if st.xpiURL == "" || st.sumsURL == "" {
		return nil, ""
	}
	targets := staleXPIBrowsers(fetchExtensionInfo(), st.LatestVersion)
	if len(targets) == 0 {
		return nil, ""
	}

	dir, err := xpiCacheDir()
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return nil, fmt.Sprintf("; could not stage the %s extension: %v", st.LatestVersion, err)
	}
	path := filepath.Join(dir, st.xpiName)
	if err := downloadVerified(st.xpiURL, st.sumsURL, st.xpiName, path); err != nil {
		return nil, fmt.Sprintf("; the %s extension was not offered to %s: %v",
			st.LatestVersion, browserNames(targets), err)
	}

	var failed []string
	for _, t := range targets {
		if err := openXPI(t.Browser, path); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", t.Browser, err))
			continue
		}
		prompted = append(prompted, t.Browser)
	}
	if len(prompted) > 0 {
		note = fmt.Sprintf("; opened the verified %s extension in %s: the user must click Add there to finish",
			st.LatestVersion, strings.Join(prompted, ", "))
	}
	if len(failed) > 0 {
		note += fmt.Sprintf("; the verified extension is at %s, to open in %s", path, strings.Join(failed, ", "))
	}
	return prompted, note
}

// downloadVerified fetches url into path, keeping it only if its SHA-256
// matches the release's SHA256SUMS entry for name.
func downloadVerified(url, sumsURL, name, path string) error {
	want, err := expectedSum(sumsURL, name)
	if err != nil {
		return fmt.Errorf("checksum lookup failed: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".xpi-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	sum, err := downloadTo(tmp, url)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	if !strings.EqualFold(sum, want) {
		return fmt.Errorf("checksum mismatch: the download does not match the signed SHA256SUMS")
	}
	return os.Rename(tmp.Name(), path)
}

func browserNames(ts []xpiTarget) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.Browser
	}
	return strings.Join(names, ", ")
}
