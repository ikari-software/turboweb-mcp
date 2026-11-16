package main

import (
	"encoding/json"
	"fmt"
	"net/http"
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
// that gap: it downloads the release .xpi itself, checks it against the
// release's SHA256SUMS, and hands it to the browser, which asks the user to Add it.

const xpiAssetPrefix = "turboweb-mcp-by-ikari-extension-firefox-"

// xpiTarget is a connected browser whose installed .xpi is behind the release.
type xpiTarget struct {
	Browser string // "Zen", "Firefox": the app name `open -a` takes on macOS
	Version string // installed version, "" when the extension is too old to say
}

// staleXPIBrowsers picks the browsers that need the release .xpi: Firefox
// family, not a temporary add-on (those are swapped on disk), and behind
// latest. An extension that reports no version predates 1.18, so it is behind.
//
// skipUnreported leaves out extensions that report no installType: when this
// run just swapped extension/dist/firefox, such a pre-1.18 extension may be
// the temporary add-on loaded from there, already updated by the reload.
func staleXPIBrowsers(exts []extensionInfo, latest string, skipUnreported bool) []xpiTarget {
	var out []xpiTarget
	seen := map[string]bool{}
	for _, e := range exts {
		if e.Browser != "Zen" && e.Browser != "Firefox" {
			continue
		}
		if e.InstallType == "development" || (skipUnreported && e.InstallType == "") {
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
	fetchExtensionInfo = connectedExtensions // (exts, ok)
	openXPI            = openXPIInBrowser
	xpiCacheDir        = func() (string, error) {
		d, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(d, "turboweb-mcp"), nil
	}
)

// connectedExtensions reports the extensions connected to the process that
// holds the browser connections, and whether it could find out. An MCP server
// asks the daemon over its relay; the daemon (auto-update) reads its own
// connections; the --self-update CLI, which has neither, asks the daemon's
// /extensions endpoint. A daemon from before 1.18 answers none of these with
// extension data, so ok is false until it has been restarted on the new build.
func connectedExtensions() (exts []extensionInfo, ok bool) {
	if useRelay {
		raw, err := send("connection_status", nil, 5000)
		if err != nil {
			return nil, false
		}
		var st struct {
			Extensions *[]extensionInfo `json:"extensions"`
		}
		if json.Unmarshal(raw, &st) != nil || st.Extensions == nil {
			return nil, false
		}
		return *st.Extensions, true
	}
	if open := getOpenBrowsers(); len(open) > 0 {
		return extensionsOf(open), true
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/extensions", wsPort))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&exts) != nil {
		return nil, false
	}
	return exts, true
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

// installStaleXPIs downloads and checks the release .xpi and opens it in
// every connected browser whose installed copy is behind. It returns the
// browsers it prompted and a note for the result message: empty only when
// every connected Firefox-family extension is known to be current.
func installStaleXPIs(st *updateStatus, firefoxDistSwapped bool) (prompted []string, note string) {
	if st.xpiURL == "" || st.sumsURL == "" {
		return nil, fmt.Sprintf("; release %s has no Firefox .xpi, so installed Firefox/Zen extensions were not checked", st.LatestVersion)
	}
	exts, ok := fetchExtensionInfo()
	if !ok {
		return nil, "; could not read which browser extensions are connected (the daemon may still run an older build), " +
			"so no .xpi was offered: restart the MCP server, then call self_update again"
	}
	targets := staleXPIBrowsers(exts, st.LatestVersion, firefoxDistSwapped)
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
		note = fmt.Sprintf("; ACTION NEEDED: opened the checksum-verified %s extension in %s, and the user must click Add there to finish",
			st.LatestVersion, strings.Join(prompted, ", "))
	}
	if len(failed) > 0 {
		note += fmt.Sprintf("; the checksum-verified extension is at %s for the user to open in %s", path, strings.Join(failed, ", "))
	}
	return prompted, note
}

// downloadVerified fetches url into path, keeping it only if its SHA-256
// matches the release's SHA256SUMS entry for name. SHA256SUMS comes from the
// same release, so this catches corrupt or swapped assets, not a forged
// release; its cosign signature is not checked here.
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
		return fmt.Errorf("checksum mismatch: the download does not match the release's SHA256SUMS")
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
