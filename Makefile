BINARY = turboweb-mcp-by-ikari
# Single source of truth for the version: the repo-root VERSION file.
# main.go gets it via ldflags; extension/build.js reads the same file.
VERSION := $(shell cat VERSION)
GITHUB_REPO = ikari-software/turboweb-mcp

# Install prefix — defaults to the Homebrew prefix when `brew` is on PATH
# (so installs land in /opt/homebrew/bin on Apple Silicon and
# /usr/local/bin on Intel macs and Linux), and otherwise falls back to
# /usr/local. Override with `make install PREFIX=/path`.
PREFIX ?= $(shell brew --prefix 2>/dev/null || echo /usr/local)

.PHONY: build install release notarize-darwin clean test test-go test-extension extension extension-watch extension-zip extension-xpi chrome-store firefox-updates-json watch

# Local dev binary lives in bin/. Release archives (zips, signed .xpi,
# cross-compiled binaries) live in dist/.
#
# On macOS we re-sign the binary with an adhoc signature after the link
# step. Go's own adhoc/linker signature has a tendency to get rejected
# by amfid (Apple's mobile file integrity daemon) after a `cp` to a
# different path — exec returns SIGKILL with no stderr. Re-signing in
# place gives the binary a fresh cdhash amfid accepts.
build: extension
	go build -ldflags="-s -w -X main.serverVersion=$(VERSION)" -o bin/$(BINARY) .
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
#   - dist/SHA256SUMS covering the (notarized) darwin binary + all other
#     artifacts; CI overwrites this with a cosign-signed authoritative copy.
#
# macOS notarization (required so Gatekeeper accepts downloaded binaries):
#   The release.yml CI workflow handles notarization automatically when the
#   following repository secrets are configured (Settings → Secrets → Actions):
#     APPLE_DEVELOPER_ID             "Developer ID Application: Name (TEAMID)"
#     APPLE_DEVELOPER_ID_CERT_P12    base64-encoded .p12 of the cert + private key
#     APPLE_DEVELOPER_ID_CERT_PASSWORD  password set when exporting the .p12
#     APPLE_ID                       Apple ID email
#     APP_SPECIFIC_PASSWORD          app-specific password from appleid.apple.com
#   Without those secrets CI produces an ad-hoc signed darwin binary and prints
#   a warning; Gatekeeper will reject downloaded copies (SIGKILL, exit 137).
#
#   Local notarization (optional, for testing): run `make notarize-darwin`
#   with APPLE_DEVELOPER_ID and either APPLE_NOTARY_PROFILE (a keychain
#   profile from `xcrun notarytool store-credentials`) or the three vars:
#     APPLE_ID  APP_SPECIFIC_PASSWORD  APPLE_TEAM_ID
#
# Old versioned XPIs accumulate in dist/ across releases; purge them first so
# only the current version's artifacts land in dist/*, then upload with:
#   gh release create vX.Y.Z \
#     dist/$(BINARY)-darwin-arm64 dist/$(BINARY)-linux-amd64 \
#     dist/$(BINARY)-windows-amd64.exe dist/$(BINARY)-windows-arm64.exe \
#     dist/$(BINARY)-extension-chrome.zip dist/$(BINARY)-extension-firefox.zip \
#     dist/$(BINARY)-extension-firefox-$(VERSION).xpi \
#     dist/firefox-updates.json dist/SHA256SUMS \
#     --title "vX.Y.Z — …" --notes "…"
release: extension extension-zip extension-xpi firefox-updates-json
	@mkdir -p dist
	@# Remove stale versioned XPIs from prior releases so dist/* stays clean.
	@find dist -maxdepth 1 -name '$(BINARY)-extension-firefox-*.xpi' \
		! -name '$(BINARY)-extension-firefox-$(VERSION).xpi' -delete 2>/dev/null; true
	@find dist -maxdepth 1 -name '$(BINARY)-chrome-store-*.zip' -delete 2>/dev/null; true
	GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w -X main.serverVersion=$(VERSION)" -o dist/$(BINARY)-darwin-arm64 .
	@$(MAKE) --no-print-directory notarize-darwin
	GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w -X main.serverVersion=$(VERSION)" -o dist/$(BINARY)-linux-amd64 .
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.serverVersion=$(VERSION)" -o dist/$(BINARY)-windows-amd64.exe .
	GOOS=windows GOARCH=arm64 go build -ldflags="-s -w -X main.serverVersion=$(VERSION)" -o dist/$(BINARY)-windows-arm64.exe .
	@# SHA256SUMS after notarization so the hash covers the signed binary.
	@cd dist && shasum -a 256 \
		$(BINARY)-darwin-arm64 \
		$(BINARY)-linux-amd64 \
		$(BINARY)-windows-amd64.exe \
		$(BINARY)-windows-arm64.exe \
		$(BINARY)-extension-chrome.zip \
		$(BINARY)-extension-firefox-$(VERSION).xpi \
		firefox-updates.json > SHA256SUMS
	@echo "sha256sums: dist/SHA256SUMS (local; authoritative copy is cosign-signed by CI)"

# Sign and notarize the macOS darwin-arm64 binary for Gatekeeper acceptance.
# Called automatically by `make release` — can also be run standalone after a
# manual build if the binary needs to be re-signed.
#
# With APPLE_DEVELOPER_ID set:
#   codesign --options runtime --timestamp  (required for notarization)
#   xcrun notarytool submit --wait          (registers ticket with Apple OCSP)
#   No staple — plain binaries can't be stapled; Gatekeeper does an online
#   OCSP check on first run instead.
# Without APPLE_DEVELOPER_ID:
#   Falls back to ad-hoc signing. Fine for local `make build`/`make install`
#   (no quarantine xattr), but Gatekeeper rejects downloaded binaries.
notarize-darwin:
	@if [ -z "$$APPLE_DEVELOPER_ID" ]; then \
		codesign -s - --force dist/$(BINARY)-darwin-arm64 2>/dev/null || true; \
		echo "notarize-darwin: APPLE_DEVELOPER_ID not set — ad-hoc only (Gatekeeper will reject downloads)"; \
	else \
		echo "notarize-darwin: signing dist/$(BINARY)-darwin-arm64 with Developer ID …"; \
		codesign --sign "$$APPLE_DEVELOPER_ID" \
			--options runtime --timestamp --force \
			dist/$(BINARY)-darwin-arm64 && \
		echo "notarize-darwin: submitting to Apple notarization service (may take ~1 min) …" && \
		tmpzip=$$(mktemp /tmp/notarize-XXXXXX.zip) && rm -f "$$tmpzip" && \
		zip -j "$$tmpzip" dist/$(BINARY)-darwin-arm64 && \
		if [ -n "$$APPLE_NOTARY_PROFILE" ]; then \
			xcrun notarytool submit "$$tmpzip" \
				--keychain-profile "$$APPLE_NOTARY_PROFILE" \
				--wait; \
		else \
			xcrun notarytool submit "$$tmpzip" \
				--apple-id "$$APPLE_ID" \
				--password "$$APP_SPECIFIC_PASSWORD" \
				--team-id "$$APPLE_TEAM_ID" \
				--wait; \
		fi; \
		rm -f "$$tmpzip"; \
		echo "notarize-darwin: done — ticket registered with Apple OCSP"; \
	fi

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
