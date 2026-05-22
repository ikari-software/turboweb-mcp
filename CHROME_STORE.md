# Chrome Web Store submission

How to get the TurboWeb MCP extension onto the Chrome Web Store (CWS).

## Why this is needed

Chrome extensions **cannot self-host auto-update** the way the Firefox build
does (`update_url` + `firefox-updates.json`). Self-hosted updates are a
Firefox feature and a Chrome *enterprise-policy* feature only. For ordinary
users, the **only** auto-update channel for a Chrome extension is the Chrome
Web Store: once published there, Chrome updates the extension from the store
automatically. So "Chrome auto-update" == "publish to CWS".

## Build the store package

The store package must have `manifest.json` at the **zip root** (the
`make extension-zip` artifact nests it under `chrome/` — that layout is for
"load unpacked", not for CWS). Use the dedicated target:

```bash
make chrome-store      # → dist/turboweb-mcp-by-ikari-chrome-store-<version>.zip
```

Upload that zip in the CWS dashboard.

## One-time setup

1. **Developer account** — register at
   <https://chrome.google.com/webstore/devconsole> with a Google account.
   One-time **US$5** registration fee.
2. **Verified contact email** and (for the data-disclosure form) a hosted
   **privacy policy URL**. The landing page under `landing/` is a natural
   home for it — add a `/privacy` page before submitting.

## Listing assets to prepare

| Asset | Requirement | Status |
|-------|-------------|--------|
| Icon | 128×128 PNG | ✅ `extension/icons/app-icon-128.png` |
| Screenshots | 1–5, 1280×800 or 640×400 PNG | ⬜ capture the popup + the on-page overlay |
| Small promo tile | 440×280 PNG | ⬜ optional but recommended |
| Short description | ≤132 chars | ⬜ draft below |
| Detailed description | plain text | ⬜ adapt `README.md` |
| Category | "Developer Tools" | — |
| Language | English | — |

Draft short description:
> Fast browser control over MCP — screenshots, DOM OCR, spatial awareness,
> and an on-page agent cursor so you can watch what the AI does.

## Permission justifications (CWS requires one per item)

CWS review asks for a written justification for every permission. Be ready:

| Permission | Justification |
|------------|---------------|
| `tabs` | Enumerate and target browser tabs for the automation commands the MCP server issues. |
| `activeTab` | Act on the tab the user is currently viewing. |
| `scripting` | Inject the content script that performs DOM reads, clicks, and typing. |
| `debugger` | Drive trusted input via the Chrome DevTools Protocol (`cdp_click`, `cdp_type`, `set_input_files`) — synthetic DOM events cannot produce `isTrusted` input. |
| `alarms` | Keep the MV3 service worker's WebSocket alive with a periodic health-check. |
| `host_permissions: <all_urls>` | The user directs automation at arbitrary sites; the extension cannot know the target host ahead of time. |

## ⚠️ Review-risk reality check

Two declared items draw **heightened CWS review** and can slow approval or
trigger rejection:

- **`debugger` permission** — CWS treats it as high-risk (it can attach to
  any page). Extensions that use it face manual review and must justify it
  precisely. The justification above (trusted-input automation) is the
  honest one; expect questions.
- **`<all_urls>` host permission** — the "broad host permissions" review
  tier. CWS prefers `activeTab` or specific match patterns. `<all_urls>` is
  genuinely required here, but it lengthens review.

This extension is a developer-automation tool, not a consumer extension —
that framing (Category: Developer Tools, clear description that a human runs
it deliberately alongside an MCP client) helps. Consider an **unlisted**
publish first (link-only, lighter discovery scrutiny) before going public.

## Data disclosure

CWS's data-use form will ask what the extension collects. TurboWeb sends page
content to the **local MCP daemon only** (`ws://127.0.0.1:18321`) — no
remote servers, no analytics. Declare: no data sold, no data used for
unrelated purposes; data handled locally for the extension's core function.
Link the privacy policy URL.

## After approval

Chrome auto-updates published extensions within a few hours of a new version
appearing in the store. The release flow becomes: bump the version, `make
chrome-store`, upload the new zip to the CWS dashboard, submit for review.
There is no `update_url` to manage — the store is the update channel.
