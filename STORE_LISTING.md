# Chrome Web Store listing — copy deck

Everything review asks you to type into the Developer Dashboard, in the
order the form asks for it. Copy-paste ready: no markdown, no smart quotes
in the fields that forbid them.

Companion doc: [`CHROME_STORE.md`](CHROME_STORE.md) covers the build, the
package layout, and the submission mechanics.

---

## Name

```
TurboWeb MCP by ikari
```

## Short description (≤132 characters) — 130 ✓

```
Fast browser control over MCP — screenshots, DOM OCR, spatial awareness, and an on-page agent cursor so you can watch the AI work.
```

Alternates if the em dash causes trouble in the field (both fit):

```
Fast browser control over MCP: screenshots, DOM OCR, spatial awareness, and a live agent cursor so you can watch the AI work.  (125)
Fast browser control for MCP — screenshots, DOM reads, real input, and a live agent cursor so you can watch the AI work.      (120)
```

## Category

```
Developer Tools
```

## Language

```
English (United States)
```

---

## Detailed description

```
TurboWeb MCP drives a real browser from an MCP client — and shows you what it is doing.

The extension is one half of a pair. It connects to a companion MCP server running on your own machine, which speaks to an AI editor over stdio — Claude Code, Cursor, Claude Desktop, or anything else that speaks the Model Context Protocol. Your editor asks, the server relays, the extension acts.

THE PART YOU CAN WATCH

Every action is visible on the page: a badge, an animated cursor that arcs to its target, and a ripple whose colour names the verb — orange for clicks, blue for typing, green for scrolling, purple for navigation, red for errors. Reads get their own treatment: text-search and text-extraction tools sweep a loupe across each match, and the interactive map flashes an outline over every clickable element on the page. The cursor fades out after 45 seconds idle.

Nothing happens until the cursor arrives. The DOM action is gated on the pointer reaching its target, so a click lands at the cursor rather than while it is still flying across the page. When a tool fails, the cursor turns red, shakes, and a toast shows the error.

REAL INPUT, NOT SYNTHETIC

Some controls only respond to trusted events — portals, popovers, anything that checks isTrusted. Those go through the Chrome DevTools Protocol, so they behave the way a human click does. Cheap synthetic DOM events are still used where they are enough, and when a click looks likely to be gated on trust the result carries a hint pointing at the CDP path instead.

EVERY CALL SAYS WHAT IT IS DOING

The intent argument is required on every tool. The model writes one sentence before acting, and it appears as a toast on the page and as the leading text of the activity row in the popup. Treat it as the model's claim about what it is doing — the parameters are right there next to it if you want to check.

THE POPUP

Lists every connected editor by session label with a client-type chip, and keeps an expandable activity log — intent, parameters, result summary — with a filter box. A pop-out button detaches it into a resizable window you can park on a second monitor.

SELF-HOSTED, NO ACCOUNT

The server is a single Go binary, downloaded from GitHub Releases. It binds to 127.0.0.1 and rejects non-localhost origins. There is no account, no sign-up, and no telemetry. Works with Chrome, Arc, Brave, Edge and Firefox; where WebDriver BiDi is available, the same tools work cross-browser without an extension at all.

Open source under Apache 2.0: https://github.com/ikari-software/turboweb-mcp
```

---

## Permission justifications

CWS asks one justification per declared permission, plus the two
first-person questions at the bottom of the Privacy tab.

| Permission | Justification to paste |
|---|---|
| `tabs` | Enumerate and target browser tabs so the connected MCP client can address a specific tab — its title, URL and index are needed to pick the right target and to move it between windows. |
| `activeTab` | Act on the tab the user is currently viewing when a tool targets "the current tab" rather than a named one. |
| `scripting` | Inject the content script that performs DOM reads, scrolling, and synthetic clicks and typing in the page the user is working on. |
| `debugger` | Dispatch trusted input and read DevTools protocol data. Synthetic DOM events cannot produce `event.isTrusted`, so popovers, portals, file pickers and any control that checks trust do not respond to them; the DevTools protocol is the only way to drive those. It is also how screenshots, console messages and network activity are captured. |
| `alarms` | Keeps the Manifest V3 service worker's WebSocket to the local server alive. MV3 workers are evicted after 30 seconds idle, which would silently drop the connection mid-session; a periodic alarm re-arms the keepalive. |
| `storage` | Persists connection preferences and per-agent approvals: a locally generated instance id, an "ask before connecting" preference, and the list of MCP client sessions the user has allowed or refused. All of it stays in the browser's local and session storage and is never transmitted. |
| `host_permissions: <all_urls>` | The user decides which page to automate at the moment they issue the command. A developer tool cannot know the target host ahead of time, and narrowing the pattern would mean the user cannot point it at their own localhost, staging or intranet pages — which is the main thing this is for. |
| Content script on `<all_urls>` | The overlay, the hover/click targeting, and the DOM tools must be present in whatever page the user has open. It is injected at `document_idle`, adds no network requests, and does nothing until the local server sends it a command. |

## Data-use answers (Privacy tab)

- **Does this extension collect or use user data?** — *Yes, but only locally.*
  Page content, screenshots and DOM data are read and handed to the MCP
  server on `127.0.0.1`. They are not sent to the publisher or to any
  third party by the extension.
- **Data sold to third parties** — No.
- **Data used or transferred for purposes unrelated to the item's core
  functionality** — No.
- **Data used or transferred to determine creditworthiness or for lending** —
  No.
- **Certification** — the item complies with the Limited Use requirements.

⚠️ The honest caveat, which the privacy policy states in full: the
**server** (not the extension) can be configured by the user with an API
key for Anthropic or Google Gemini to summarise tool results. That call
goes out from the server process, on the user's own key, only when they
configure it. The extension itself never talks to a remote host.

## Privacy policy URL

```
https://ikari-software.github.io/turboweb-mcp/privacy.html
```

## Support / homepage

```
https://ikari-software.github.io/turboweb-mcp/
https://github.com/ikari-software/turboweb-mcp
```

---

## Screenshots

All five are captured and checked in under [`store/`](store/). Upload them
in this order — CWS shows slot 1 first, and the overlay is the shot that
explains the product in one glance.

| Slot | File | What it shows |
|---|---|---|
| 1 | [`store/screenshot-1-overlay.png`](store/screenshot-1-overlay.png) | The on-page overlay mid-action: amber badge reading the intent, the cursor parked on its target, amber ripple on the click |
| 2 | [`store/screenshot-2-popup.png`](store/screenshot-2-popup.png) | The popup: connected-agents list, activity log with two rows expanded |
| 3 | [`store/screenshot-3-read.png`](store/screenshot-3-read.png) | A read-only tool visualised: the loupe sweeping each `find_text` hit |
| 4 | [`store/screenshot-4-popout.png`](store/screenshot-4-popout.png) | The pop-out window, detached and resizable |
| 5 | [`store/screenshot-5-error.png`](store/screenshot-5-error.png) | Error state: red cursor, red ripple, `✗ Element not found: .archive-link` toast |

All five are 1280×800 PNG, which satisfies the 1280×800 requirement
directly — no letterboxing or rescaling.

Screenshots must show the extension in use and must not contain
unreadable text, marketing claims, or another product's branding. The
subject page in every shot is a fixture we control; no third-party site
appears in any of them.

### Re-capturing

The shots were taken against a local fixture page in a Chrome for Testing
window driven over CDP, with the extension loaded unpacked. Two details
matter if you redo them, because both fail silently:

- **`Chrome 143+` ignores `--load-extension`.** Use Chrome for Testing, or
  the payload will not load and the popup will render empty.
- **The popup skips rebuilding its log while `document.hidden` is true**, so
  a background tab photographs as an empty list no matter how many commands
  ran. Bring the tab to front inside the same CDP session that captures it —
  `Emulation.setDeviceMetricsOverride` does not survive across sessions.

## Small promo tile

[`store/promo-tile-440x280.png`](store/promo-tile-440x280.png) — 440×280,
the logomark and wordmark on the brand background with the line
*"Watch the AI work in your browser."* Drawn as a layout, not a scaled
screenshot, which is what CWS wants here.

---

## Before you upload — checklist

- [x] **Drop `webNavigation` from `extension/manifest.json`.** It was declared
      but referenced nowhere in the extension source. An unused permission
      is an unjustifiable permission, and unjustifiable permissions are a
      rejection reason. **Done** — removed.
- [x] `make chrome-store` produces the zip with `manifest.json` at the root.
      **Done** — `dist/turboweb-mcp-by-ikari-chrome-store-1.15.0.zip`, and
      both the workflow and `scripts/publish-chrome-store.sh` assert it.
- [x] Confirm the stamped `version` is the release you mean to ship — CWS
      rejects an upload whose version is not greater than the published one.
      **Done for the current tree** — `VERSION` and the packaged manifest
      both read 1.15.0. The publishing script re-checks this against the
      live published version at upload time, so a stale bump fails there
      rather than after review.
- [x] Screenshots captured **after** a rebuild, so the UI in them matches
      the submitted package. Note shot 4's popup still reads `v1.5.0` from
      the build it was captured against; refresh it if the version string in
      the popup is part of what you want to advertise.
- [x] Privacy policy URL resolves publicly (200, not a login wall).
      **Done** — `landing/privacy.html` is on master, `pages.yml` deployed
      it, and `https://ikari-software.github.io/turboweb-mcp/privacy.html`
      returns 200 with no login wall. Safe to paste into the dashboard.
