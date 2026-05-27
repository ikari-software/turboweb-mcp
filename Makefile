BINARY = turboweb-mcp-by-ikari
VERSION = 1.8.1
GITHUB_REPO = ikari-software/turboweb-mcp

# Install prefix — defaults to the Homebrew prefix when `brew` is on PATH
# (so installs land in /opt/homebrew/bin on Apple Silicon and
# /usr/local/bin on Intel macs and Linux), and otherwise falls back to
# /usr/local. Override with `make install PREFIX=/path`.
PREFIX ?= $(shell brew --prefix 2>/dev/null || echo /usr/local)

.PHONY: build install release clean test test-go test-extension extension extension-watch extension-zip extension-xpi chrome-store firefox-updates-json watch

# Local dev binary lives in bin/. Release archives (zips, signed .xpi,
# cross-compiled binaries) live in dist/.
#
# On macOS we re-sign the binary with an adhoc signature after the link
# step. Go's own adhoc/linker signature has a tendency to get rejected
# by amfid (Apple's mobile file integrity daemon) after a `cp` to a
# different path — exec returns SIGKILL with no stderr. Re-signing in
# place gives the binary a fresh cdhash amfid accepts.
build: extension
	go build -ldflags="-s -w" -o bin/$(BINARY) .
	@if [ "$$(uname)" = "Darwin" ]; then codesign -s - --force bin/$(BINARY) >/dev/null 2>&1; fi

install: build
	mkdir -p $(PREFIX)/bin
	cp bin/$(BINARY) $(PREFIX)/bin/
	@if [ "$$(uname)" = "Darwin" ]; then codesign -s - --force $(PREFIX)/bin/$(BINARY) >/dev/null 2>&1; fi
	@echo "installed: $(PREFIX)/bin/$(BINARY)"

# `make release` produces every artifact a GitHub release should ship into
# dist/:
#   - cross-compiled Go binaries for darwin/linux/windows
#   - extension/dist/{chrome,firefox} zipped (self-contained installs that
#     don't need a local Node toolchain)
#   - dist/*.xpi (AMO-signed) when WEB_EXT_API_KEY / WEB_EXT_API_SECRET are
#     set; skipped otherwise so a local `make release` without credentials
#     still succeeds.
#
# Upload with `gh release create vX.Y.Z dist/*`.
release: extension extension-zip extension-xpi firefox-updates-json
	@mkdir -p dist
	GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o dist/$(BINARY)-darwin-arm64 .
	GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o dist/$(BINARY)-linux-amd64 .
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o dist/$(BINARY)-windows-amd64.exe .
	GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o dist/$(BINARY)-windows-arm64.exe .

# One-shot rebuild of the loadable extension into extension/dist/{chrome,firefox}/.
extension:
	cd extension && node build.js

# Watch the extension source files and rebuild dist/ on every change. Load
# extension/dist/chrome/ in chrome://extensions and click "Reload" on the
# extension card after each change to pick up new code in the browser.
extension-watch watch:
	cd extension && node build.js --watch

# Zip the loadable extension folders so a release artifact is self-contained.
# A user can download turboweb-mcp-by-ikari-extension-chrome.zip, unzip it,
# and load the unpacked folder via chrome://extensions — no Node, no Make.
extension-zip: extension
	mkdir -p dist
	cd extension/dist && rm -f ../../dist/$(BINARY)-extension-chrome.zip ../../dist/$(BINARY)-extension-firefox.zip
	cd extension/dist && zip -qr ../../dist/$(BINARY)-extension-chrome.zip chrome
	cd extension/dist && zip -qr ../../dist/$(BINARY)-extension-firefox.zip firefox

# Store-ready Chrome zip. Unlike extension-zip (which nests everything
# under chrome/ for "load unpacked"), the Chrome Web Store requires
# manifest.json at the zip ROOT — so this zips the *contents* of the
# built chrome/ dir. See CHROME_STORE.md for the submission flow.
chrome-store: extension
	mkdir -p dist
	rm -f "dist/$(BINARY)-chrome-store-$(VERSION).zip"
	cd extension/dist/chrome && zip -qr "$(CURDIR)/dist/$(BINARY)-chrome-store-$(VERSION).zip" .
	@echo "chrome-store: dist/$(BINARY)-chrome-store-$(VERSION).zip"

# Produce an AMO-signed .xpi from extension/dist/firefox/ via web-ext sign.
# Requires WEB_EXT_API_KEY and WEB_EXT_API_SECRET from
# https://addons.mozilla.org/en-US/developers/addon/api/key/ — without them
# this target prints a skip notice and exits 0 so local builds still work.
# Channel defaults to 'unlisted' (self-distributed signing). Set
# WEB_EXT_CHANNEL=listed to submit to AMO review.
extension-xpi: extension
	@if [ -z "$$WEB_EXT_API_KEY" ] || [ -z "$$WEB_EXT_API_SECRET" ]; then \
		echo "extension-xpi: skipping (WEB_EXT_API_KEY / WEB_EXT_API_SECRET not set)"; \
	else \
		mkdir -p dist && \
		rm -f "dist/$(BINARY)-extension-firefox-$(VERSION).xpi" && \
		(cd extension && npx --no-install web-ext sign \
			--source-dir=dist/firefox \
			--artifacts-dir=../dist \
			--channel="$${WEB_EXT_CHANNEL:-unlisted}" \
			--api-key="$$WEB_EXT_API_KEY" \
			--api-secret="$$WEB_EXT_API_SECRET") && \
		find dist -maxdepth 1 -name "*-$(VERSION).xpi" \
			-not -name "$(BINARY)-extension-firefox-*" \
			-exec mv {} "dist/$(BINARY)-extension-firefox-$(VERSION).xpi" \; ; \
	fi

# Generate dist/firefox-updates.json from the freshly signed XPI. Firefox
# polls this file (served at releases/latest/download/firefox-updates.json
# by GitHub) and auto-installs newer versions. Skipped silently when there's
# no signed XPI (e.g. local `make release` without AMO creds).
firefox-updates-json: extension-xpi
	@xpi="dist/$(BINARY)-extension-firefox-$(VERSION).xpi"; \
	if [ ! -f "$$xpi" ]; then \
		echo "firefox-updates-json: skipping (no signed XPI at $$xpi)"; \
		exit 0; \
	fi; \
	hash=$$(shasum -a 256 "$$xpi" | awk '{print $$1}'); \
	url="https://github.com/$(GITHUB_REPO)/releases/download/v$(VERSION)/$(BINARY)-extension-firefox-$(VERSION).xpi"; \
	printf '{\n  "addons": {\n    "turboweb-mcp@ikari.pl": {\n      "updates": [\n        { "version": "%s", "update_link": "%s", "update_hash": "sha256:%s" }\n      ]\n    }\n  }\n}\n' \
	  "$(VERSION)" "$$url" "$$hash" > dist/firefox-updates.json; \
	echo "firefox-updates-json: dist/firefox-updates.json -> $$url"

test: test-go test-extension

test-go:
	go test ./...

test-extension:
	cd extension && npm test

clean:
	rm -rf bin/$(BINARY)* dist extension/coverage extension/dist
