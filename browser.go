package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// BrowserConfig controls auto-launch behavior.
type BrowserConfig struct {
	// AutoLaunch: start a stealth browser if none connected when a tool is called.
	AutoLaunch bool `json:"autoLaunch"`
	// ChromePath: explicit path to Chrome binary. Auto-detected if empty.
	ChromePath string `json:"chromePath,omitempty"`
	// UserDataDir: Chrome profile directory. Uses a dedicated one if empty.
	UserDataDir string `json:"userDataDir,omitempty"`
	// Headless: launch in headless mode.
	Headless bool `json:"headless,omitempty"`
	// ExtraArgs: additional Chrome flags.
	ExtraArgs []string `json:"extraArgs,omitempty"`
}

var browserConfig *BrowserConfig

func loadBrowserConfig() {
	browserConfig = &BrowserConfig{AutoLaunch: true} // default: auto-launch enabled
	configPath := filepath.Join(getConfigDir(), "browser.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// Write default config for discoverability
		def, _ := json.MarshalIndent(browserConfig, "", "  ")
		os.MkdirAll(getConfigDir(), 0755)
		os.WriteFile(configPath, def, 0644)
		return
	}
	json.Unmarshal(data, browserConfig)
}

// launchBrowser starts a browser with stealth flags, the extension loaded, and BiDi enabled.
func launchBrowser(headless bool) (int, error) {
	chromePath := browserConfig.ChromePath
	if chromePath == "" {
		chromePath = findChrome()
	}
	if chromePath == "" {
		return 0, fmt.Errorf("browser not found. Set chromePath in %s/browser.json", getConfigDir())
	}

	firefox := isFirefoxPath(chromePath)

	// Extension path: same directory as our binary, or adjacent "extension" dir
	extPath := findExtensionDir()

	userDataDir := browserConfig.UserDataDir
	if userDataDir == "" {
		userDataDir = filepath.Join(getConfigDir(), "chrome-profile")
	}

	// Find a free port for BiDi/remote debugging
	debugPort := findFreePort()

	var args []string
	if firefox {
		args = []string{
			"--profile", userDataDir,
			"--no-remote",
			fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		}
		if headless || browserConfig.Headless {
			args = append(args, "--headless")
		}
	} else {
		// Chromium-based browser
		args = []string{
			"--silent-debugger-extension-api",
			"--user-data-dir=" + userDataDir,
			"--no-first-run",
			"--no-default-browser-check",
			"--remote-allow-origins=*",
			fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		}
		if extPath != "" {
			args = append(args, "--load-extension="+extPath)
		}
		if headless || browserConfig.Headless {
			args = append(args, "--headless=new")
		}
	}

	args = append(args, browserConfig.ExtraArgs...)
	args = append(args, "about:blank")

	cmd := exec.Command(chromePath, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to launch browser: %w", err)
	}

	go cmd.Wait()

	pid := cmd.Process.Pid
	browserName := browserDisplayName(chromePath)
	logger.Printf("Launched %s (pid %d, debug port %d)", browserName, pid, debugPort)
	if firefox && extPath != "" {
		logger.Printf("Firefox: unpacked extension cannot be auto-loaded from CLI; install %s once via about:debugging (This Firefox).", extPath)
	}

	// Connect BiDi in the background (non-blocking)
	go connectBiDiWithRetry(debugPort)

	// Wait for extension connection (Chromium) or BiDi connection (Firefox)
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		hasExtension := len(getOpenBrowsers()) > 0
		hasBiDi := getBiDi() != nil
		if hasExtension || hasBiDi {
			logger.Printf("%s connected after %dms (extension=%v, bidi=%v)", browserName, (i+1)*200, hasExtension, hasBiDi)
			return pid, nil
		}
	}

	return pid, fmt.Errorf("browser launched but no connection established within 10s")
}

// ensureBrowser auto-launches Chrome if autoLaunch is enabled and no browsers connected.
func ensureBrowser() error {
	if len(getOpenBrowsers()) > 0 {
		return nil
	}
	if browserConfig == nil || !browserConfig.AutoLaunch {
		return nil
	}
	_, err := launchBrowser(false)
	return err
}

// isFirefoxPath returns true if the binary path looks like a Firefox-family executable.
func isFirefoxPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(base, "firefox") ||
		isZenPath(path) ||
		strings.Contains(base, "librewolf") ||
		strings.Contains(base, "waterfox")
}

func browserDisplayName(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case isZenPath(path):
		return "Zen"
	case strings.Contains(base, "firefox"):
		return "Firefox"
	case strings.Contains(base, "librewolf"):
		return "LibreWolf"
	case strings.Contains(base, "waterfox"):
		return "Waterfox"
	case strings.Contains(base, "brave"):
		return "Brave"
	case strings.Contains(base, "arc"):
		return "Arc"
	case strings.Contains(base, "edge") || strings.Contains(base, "msedge"):
		return "Edge"
	case strings.Contains(base, "chromium"):
		return "Chromium"
	default:
		return "Chrome"
	}
}

func isZenPath(path string) bool {
	lower := strings.ToLower(path)
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	return base == "zen" ||
		strings.HasPrefix(base, "zen-") ||
		strings.Contains(lower, "zen browser.app") ||
		strings.Contains(lower, "zen browser")
}

// findFreePort returns a free TCP port by binding to :0 and reading the assigned port.
func findFreePort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 9222 // fallback
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// findChrome locates the Chrome/Firefox binary on the current platform.
func findChrome() string {
	switch runtime.GOOS {
	case "darwin":
		paths := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Arc.app/Contents/MacOS/Arc",
			"/Applications/Zen Browser.app/Contents/MacOS/zen",
			"/Applications/Firefox.app/Contents/MacOS/firefox",
			"/Applications/Firefox Developer Edition.app/Contents/MacOS/firefox",
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	case "linux":
		names := []string{"google-chrome", "google-chrome-stable", "chromium-browser", "chromium", "brave-browser", "zen-browser", "zen", "firefox", "firefox-developer-edition"}
		for _, name := range names {
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
		}
	case "windows":
		paths := []string{
			filepath.Join(os.Getenv("PROGRAMFILES"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Zen Browser", "zen.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES"), "Zen Browser", "zen.exe"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// findExtensionDir locates the extension directory relative to the binary.
func findExtensionDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	// Check adjacent "extension" directory
	dir := filepath.Dir(exe)
	extDir := filepath.Join(dir, "extension")
	if _, err := os.Stat(filepath.Join(extDir, "manifest.json")); err == nil {
		return extDir
	}
	// Check parent directory (for development: binary in repo root, extension/ alongside)
	extDir = filepath.Join(dir, "..", "extension")
	if _, err := os.Stat(filepath.Join(extDir, "manifest.json")); err == nil {
		return extDir
	}
	// Check cwd
	if cwd, err := os.Getwd(); err == nil {
		extDir = filepath.Join(cwd, "extension")
		if _, err := os.Stat(filepath.Join(extDir, "manifest.json")); err == nil {
			return extDir
		}
	}
	return ""
}
