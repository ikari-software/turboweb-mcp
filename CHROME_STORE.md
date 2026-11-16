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
   **privacy policy URL**. The page is [`landing/privacy.html`](landing/privacy.html),
   served by `.github/workflows/pages.yml` as
   `https://ikari-software.github.io/turboweb-mcp/privacy.html` — live and
   returning 200. Note `pages.yml` only runs on pushes that touch `landing/`,
   so an edit to the page needs a commit under that path to go out.

## Listing assets to prepare

All of these are captured and checked in; see
[`STORE_LISTING.md`](STORE_LISTING.md) for the exact copy to paste into each
dashboard field.

| Asset | Requirement | Status |
|-------|-------------|--------|
| Icon | 128×128 PNG | ✅ `extension/icons/app-icon-128.png` |
| Screenshots | 1–5, 1280×800 PNG | ✅ `store/screenshot-{1..5}-*.png` |
| Small promo tile | 440×280 PNG | ✅ `store/promo-tile-440x280.png` |
| Short description | ≤132 chars | ✅ `STORE_LISTING.md` |
| Detailed description | plain text | ✅ `STORE_LISTING.md` |
| Category | "Developer Tools" | — |
| Language | English | — |

The detailed description is written for the store rather than lifted from
`README.md` — the README documents the server's tool surface, which is the
wrong pitch for someone browsing extensions.

## Permission justifications (CWS requires one per item)

CWS review asks for a written justification for every permission. The
paste-ready text lives in [`STORE_LISTING.md`](STORE_LISTING.md); the short
form:

| Permission | Justification |
|------------|---------------|
| `tabs` | Enumerate and target browser tabs for the automation commands the MCP server issues. |
| `activeTab` | Act on the tab the user is currently viewing. |
| `scripting` | Inject the content script that performs DOM reads, clicks, and typing. |
| `debugger` | Drive trusted input via the Chrome DevTools Protocol (`cdp_click`, `cdp_type`, `set_input_files`) — synthetic DOM events cannot produce `isTrusted` input. |
| `alarms` | Keep the MV3 service worker's WebSocket alive with a periodic health-check. |
| `storage` | Persist connection preferences and per-agent approvals locally. |
| `host_permissions: <all_urls>` | The user directs automation at arbitrary sites; the extension cannot know the target host ahead of time. |

`webNavigation` was declared but referenced nowhere in the source; it has
been removed. An unused permission is an unjustifiable permission, and
unjustifiable permissions are a rejection reason.

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
Link the privacy policy URL (`landing/privacy.html`).

One honest caveat belongs in that policy and the reviewer may ask about it:
the **server** — not the extension — can be configured by the user with an
Anthropic or Gemini API key to summarise tool results. That request leaves
the machine on the user's own key, only after they configure it. The
extension itself never contacts a remote host.

## After approval

Chrome auto-updates published extensions within a few hours of a new version
appearing in the store. There is no `update_url` to manage — the store is the
update channel.

### Automated publishing

`.github/workflows/chrome-web-store.yml` uploads and submits each release.
It is called at the end of `release-please.yml`, after the GitHub release is
public, and can also be dispatched on its own:

```bash
gh workflow run chrome-web-store.yml -f version=1.16.0
```

The mechanics are in `scripts/publish-chrome-store.sh`, which talks to the
**Chrome Web Store API V2**:

```
POST https://chromewebstore.googleapis.com/upload/v2/publishers/{pub}/items/{item}:upload
POST https://chromewebstore.googleapis.com/v2/publishers/{pub}/items/{item}:publish
```

Two things about V2 that are easy to get wrong, and that the old recipes
online get wrong:

- **Upload is a `POST`, not a `PUT`** (V1 used `PUT`), and the `/upload/`
  prefix is mandatory for the package body. The metadata URI without that
  prefix takes no zip even though it exists and shares the method name.
- **`publish` *is* the submit-for-review call.** There is no separate submit
  endpoint. `publishType` then decides what happens on approval:
  `DEFAULT_PUBLISH` goes live, `STAGED_PUBLISH` parks it for a manual go.

API V1 is deprecated and **scheduled for shutdown on 2026-10-15**; the scopes
are unchanged between versions, so V1 refresh tokens keep working.

Auth is a **service account**, which V2 supports and V1 did not. Create one in
Google Cloud, enable the Chrome Web Store API, then add its email under
*Account* in the Developer Dashboard — that link is what gives it authority
over the publisher's items. No IAM role or extra API is needed: the workflow
signs a JWT with the key and exchanges it at `https://oauth2.googleapis.com/token`
(the JWT-bearer grant) for an access token with the single scope
`https://www.googleapis.com/auth/chromewebstore`. It deliberately avoids
`google-github-actions/auth`, whose `token_format: access_token` path goes
through the IAM Service Account Credentials API and fails unless that API is
enabled in the project.

Secrets to add (Settings → Secrets and variables → Actions):

| Secret | Where it comes from |
|---|---|
| `CWS_SA_JSON` | the service account's JSON key, pasted verbatim |
| `CWS_PUBLISHER_ID` | `.../publishers/<id>/` in the Developer Dashboard URL |
| `CWS_ITEM_ID` | the 32-character extension id |

Without `CWS_SA_JSON` the job prints a skip notice and exits 0, the same
contract `make extension-xpi` keeps for missing AMO credentials.

**What the API cannot do** — a manual step per release, and the reason this is
not fully hands-off:

- **It cannot create items.** The first version must be uploaded by hand in
  the dashboard, along with the Store listing and Privacy tabs.
- **It cannot write listing metadata.** Description, screenshots, permission
  justifications and visibility are dashboard-only. That includes visibility:
  changing it through the API is deliberately unsupported, so if you edit it
  in the dashboard you must publish manually once with the new visibility
  before the API will work again.
- **It cannot climb over a taken-down item.** `fetchStatus` reports
  `takenDown`; the script refuses to proceed if so.

Two more preconditions the workflow verifies rather than assumes: the packaged
`VERSION` must be strictly greater than the published one (the script compares
them with `sort -V` and stops otherwise), and `manifest.json` must be at the zip
root.
