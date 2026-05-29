package main

import "testing"

func TestIsFirefoxPathRecognizesFirefoxFamily(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/Applications/Firefox.app/Contents/MacOS/firefox", true},
		{"/Applications/Zen Browser.app/Contents/MacOS/zen", true},
		{"/usr/bin/zen-browser", true},
		{`C:\Users\me\AppData\Local\Programs\Zen Browser\zen.exe`, true},
		{"/Applications/LibreWolf.app/Contents/MacOS/librewolf", true},
		{"/Applications/Waterfox.app/Contents/MacOS/waterfox", true},
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", false},
		{"/Applications/Arc.app/Contents/MacOS/Arc", false},
		{"/usr/bin/frozen", false},
	}

	for _, tt := range tests {
		if got := isFirefoxPath(tt.path); got != tt.want {
			t.Errorf("isFirefoxPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestBrowserDisplayName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/Applications/Zen Browser.app/Contents/MacOS/zen", "Zen"},
		{"/Applications/Firefox.app/Contents/MacOS/firefox", "Firefox"},
		{"/Applications/LibreWolf.app/Contents/MacOS/librewolf", "LibreWolf"},
		{"/Applications/Waterfox.app/Contents/MacOS/waterfox", "Waterfox"},
		{"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser", "Brave"},
		{"/Applications/Arc.app/Contents/MacOS/Arc", "Arc"},
		{"/Applications/Chromium.app/Contents/MacOS/Chromium", "Chromium"},
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Chrome"},
	}

	for _, tt := range tests {
		if got := browserDisplayName(tt.path); got != tt.want {
			t.Errorf("browserDisplayName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
