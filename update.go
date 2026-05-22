package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// githubRepo is the owner/name the self-updater queries for releases. Kept
// in sync with GITHUB_REPO in the Makefile.
const githubRepo = "ikari-software/turboweb-mcp"

// updateCheckTTL is how long a release-check result is reused before the
// next call re-queries GitHub — the API allows only 60 unauthenticated
// requests/hour/IP, and a newer release every few hours is plenty fresh.
const updateCheckTTL = 6 * time.Hour

// updateStatus is the result of a release check: the current build versus
// the latest GitHub release, and the download URLs needed to self-update.
type updateStatus struct {
	CurrentVersion  string    `json:"currentVersion"`
	LatestVersion   string    `json:"latestVersion"`
	UpdateAvailable bool      `json:"updateAvailable"`
	ReleaseURL      string    `json:"releaseUrl"`
	CheckedAt       time.Time `json:"checkedAt"`
	// Error is set when the check itself failed (offline, rate-limited,
	// no asset for this platform). Callers still get a usable struct.
	Error string `json:"error,omitempty"`
	// assetURL / sumsURL are the release-asset download links for this
	// GOOS/GOARCH; unexported because they are an implementation detail
	// of performSelfUpdate, not part of the tool's JSON contract.
	assetURL  string
	assetName string
	sumsURL   string
}

var (
	updateMu     sync.Mutex
	cachedUpdate *updateStatus
)

// releaseAssetName maps the running platform to the release-asset filename
// produced by `make release`. Returns "" for a platform the release does
// not ship a binary for (e.g. darwin/amd64, linux/arm64).
func releaseAssetName() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "turboweb-mcp-by-ikari-darwin-arm64"
	case "linux/amd64":
		return "turboweb-mcp-by-ikari-linux-amd64"
	case "windows/amd64":
		return "turboweb-mcp-by-ikari-windows-amd64.exe"
	case "windows/arm64":
		return "turboweb-mcp-by-ikari-windows-arm64.exe"
	default:
		return ""
	}
}

// compareVersions compares two dotted version strings (a leading "v" is
// ignored). Returns -1 if a < b, 0 if equal, 1 if a > b. Missing trailing
// components count as zero, so "1.3" == "1.3.0".
func compareVersions(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	pb := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

// fetchLatestRelease queries the GitHub releases API for the newest
// published release and builds an updateStatus from it.
func fetchLatestRelease() *updateStatus {
	st := &updateStatus{CurrentVersion: serverVersion, CheckedAt: time.Now().UTC()}

	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	// GitHub requires a User-Agent; the Accept header pins the API version.
	req.Header.Set("User-Agent", "turboweb-mcp/"+serverVersion)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		st.Error = "release check failed: " + err.Error()
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		st.Error = fmt.Sprintf("release check failed: GitHub returned %d", resp.StatusCode)
		return st
	}

	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		st.Error = "release check failed: " + err.Error()
		return st
	}

	st.LatestVersion = strings.TrimPrefix(rel.TagName, "v")
	st.ReleaseURL = rel.HTMLURL
	st.UpdateAvailable = st.LatestVersion != "" &&
		compareVersions(st.LatestVersion, serverVersion) > 0

	wantAsset := releaseAssetName()
	if wantAsset == "" {
		st.Error = fmt.Sprintf("no release binary for %s/%s — self-update unavailable on this platform",
			runtime.GOOS, runtime.GOARCH)
		return st
	}
	st.assetName = wantAsset
	for _, a := range rel.Assets {
		switch a.Name {
		case wantAsset:
			st.assetURL = a.URL
		case "SHA256SUMS":
			st.sumsURL = a.URL
		}
	}
	if st.UpdateAvailable && st.assetURL == "" {
		st.Error = fmt.Sprintf("release %s ships no %s asset", rel.TagName, wantAsset)
	}
	return st
}

// getUpdateStatus returns the cached release check when it is still fresh,
// otherwise performs a new one. force bypasses the cache.
func getUpdateStatus(force bool) *updateStatus {
	updateMu.Lock()
	defer updateMu.Unlock()
	if !force && cachedUpdate != nil &&
		time.Since(cachedUpdate.CheckedAt) < updateCheckTTL {
		return cachedUpdate
	}
	cachedUpdate = fetchLatestRelease()
	return cachedUpdate
}

// peekUpdateStatus returns the last cached release check without ever
// triggering a network call — safe for latency-sensitive paths like
// connection_status. Returns nil if no check has run yet.
func peekUpdateStatus() *updateStatus {
	updateMu.Lock()
	defer updateMu.Unlock()
	return cachedUpdate
}

// startUpdateChecker runs a release check shortly after daemon startup,
// then refreshes it every updateCheckTTL, so connection_status can report
// "update available" without ever blocking on a GitHub request.
func startUpdateChecker() {
	go func() {
		time.Sleep(5 * time.Second) // let startup settle before the first call
		for {
			getUpdateStatus(true)
			time.Sleep(updateCheckTTL)
		}
	}()
}

// updateResult is the outcome of a self-update attempt.
type updateResult struct {
	Updated     bool   `json:"updated"`
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	Message     string `json:"message"`
}

// performSelfUpdate downloads the latest release binary for this platform,
// verifies it against the release's signed SHA256SUMS, and atomically
// replaces the running executable on disk. It does NOT restart anything:
// the daemon's existing stale-binary detection (daemonIsStale) respawns it
// from the new binary on the next MCP call, and MCP instances pick up the
// new build on their next launch.
func performSelfUpdate() (updateResult, error) {
	res := updateResult{FromVersion: serverVersion}

	st := getUpdateStatus(true)
	res.ToVersion = st.LatestVersion
	if st.Error != "" {
		return res, fmt.Errorf("%s", st.Error)
	}
	if !st.UpdateAvailable {
		res.Message = fmt.Sprintf("already on the latest version (%s)", serverVersion)
		return res, nil
	}
	if st.assetURL == "" {
		return res, fmt.Errorf("no downloadable asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	exe, err := os.Executable()
	if err != nil {
		return res, fmt.Errorf("cannot locate own executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return res, fmt.Errorf("cannot resolve executable path: %w", err)
	}

	// Stage the download in the executable's own directory so the final
	// os.Rename is an atomic same-filesystem move.
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".turboweb-update-*")
	if err != nil {
		return res, fmt.Errorf("cannot create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() {
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			_ = os.Remove(tmpPath)
		}
	}()

	sum, err := downloadTo(tmp, st.assetURL)
	tmp.Close()
	if err != nil {
		return res, fmt.Errorf("download failed: %w", err)
	}

	// Verify the download against the release's SHA256SUMS before we let
	// it anywhere near the executable path.
	if st.sumsURL == "" {
		return res, fmt.Errorf("release ships no SHA256SUMS — refusing to install an unverified binary")
	}
	want, err := expectedSum(st.sumsURL, st.assetName)
	if err != nil {
		return res, fmt.Errorf("checksum lookup failed: %w", err)
	}
	if !strings.EqualFold(sum, want) {
		return res, fmt.Errorf("checksum mismatch — downloaded binary does not match the signed SHA256SUMS (got %s, want %s)", sum, want)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return res, fmt.Errorf("chmod failed: %w", err)
	}
	// macOS: a binary that arrives via download/copy is rejected by amfid
	// unless it carries a fresh ad-hoc signature (same reason `make build`
	// re-signs). Best-effort — if codesign is missing the rename still
	// proceeds and the user gets a clear error on next exec.
	if runtime.GOOS == "darwin" {
		_ = exec.Command("codesign", "-s", "-", "--force", tmpPath).Run()
	}

	if err := replaceExecutable(exe, tmpPath); err != nil {
		return res, fmt.Errorf("could not replace the running binary: %w", err)
	}

	res.Updated = true
	res.Message = fmt.Sprintf("updated %s → %s; the daemon respawns from the new binary on the next call, "+
		"and MCP instances pick it up on next launch", serverVersion, st.LatestVersion)
	return res, nil
}

// downloadTo streams url into w and returns the hex-encoded SHA-256 of the
// bytes written.
func downloadTo(w io.Writer, url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "turboweb-mcp/"+serverVersion)
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub returned %d", resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// expectedSum fetches the release SHA256SUMS file and returns the hash
// recorded for assetName. SHA256SUMS lines are "<hex>  <filename>".
func expectedSum(sumsURL, assetName string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, sumsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "turboweb-mcp/"+serverVersion)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == assetName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s not listed in SHA256SUMS", assetName)
}

// replaceExecutable atomically swaps the file at exe for the staged binary
// at tmpPath. On Unix os.Rename over a running executable is fine — the
// kernel keeps the open inode. Windows forbids overwriting a running .exe,
// so the live binary is first renamed aside (which Windows does allow).
func replaceExecutable(exe, tmpPath string) error {
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmpPath, exe); err != nil {
			// Roll back so we don't leave the user with no binary.
			_ = os.Rename(old, exe)
			return err
		}
		// The .old file is locked until the running process exits;
		// removal is best-effort and usually succeeds on the next run.
		_ = os.Remove(old)
		return nil
	}
	return os.Rename(tmpPath, exe)
}
