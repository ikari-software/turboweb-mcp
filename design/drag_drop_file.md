# Design: `drag_drop_file` — non-CDP file upload via synthetic drag-and-drop

Status: draft
Author: design doc for review
Related code: `tools_files.go`, `extension/background.js` (`setInputFiles`, `interceptFileChooser`, `NATIVE_URL`/resize path), `extension/content.js`, `ws.go`, `resize.go`

---

## 1. Problem & motivation

TurboWeb-MCP has exactly one way to put a file into a page: `set_input_files`
(`tools_files.go` → `background.js setInputFiles`), which drives
`chrome.debugger` CDP `DOM.setFileInputFiles`. `intercept_file_chooser` is a
variant of the same CDP mechanism. Both fail in two real situations:

1. **CDP is unavailable.** `chrome.debugger.attach` can be refused or
   undesirable: the user has DevTools open on the tab, an enterprise policy
   blocks the `debugger` permission, the browser is Firefox (the project
   already ships a Firefox build — `dist/firefox/` — and Firefox has no
   `chrome.debugger` equivalent for `DOM.setFileInputFiles`), or the visible
   "Chrome is being debugged" infobar is unacceptable to the user. When CDP
   is off, there is currently **no file-upload path at all**.

2. **There is no `<input type=file>` to target.** Many modern upload widgets
   (drag-drop zones built on `react-dropzone`, Uppy, Dropzone.js, FilePond,
   plain `ondrop` handlers) render only a styled drop region. Some never
   create an `<input type=file>` until a click opens the OS picker; others
   gate purely on `drop` events. `set_input_files` walks the DOM looking for
   a descendant `input[type=file]` and throws `No <input type=file> resolved`
   when there isn't one.

The web platform offers a CDP-free escape hatch: a page script can synthesize
a `DragEvent` carrying a `DataTransfer` whose `.files` list contains a real
`File` object, and dispatch `dragenter`/`dragover`/`drop` at the drop zone.
The drop-zone handler reads `event.dataTransfer.files` and proceeds exactly as
if a human dropped a file. The only missing piece is **getting the file bytes
from the MCP server host into the page** — which is solvable with the same
loopback-HTTP pattern the project already uses for the screenshot resizer
(`NATIVE_URL = http://127.0.0.1:18322`).

`drag_drop_file` is a non-CDP fallback for both cases. It also, as a bonus,
covers a CDP-free upload to a real `<input type=file>` (see §3.5).

## 2. Proposed MCP tool API

Registered in `tools_files.go` alongside `set_input_files`.

**Name:** `drag_drop_file`

**Description (tool text):** "Upload a local file by simulating a
drag-and-drop onto a drop zone — a non-CDP fallback for `set_input_files`.
Use when the upload widget is a drag-drop zone with no `<input type=file>`,
or when CDP/`chrome.debugger` is unavailable (DevTools open, Firefox,
policy-blocked). Synthesizes a real `File` in the page and dispatches
`dragenter`/`dragover`/`drop` carrying it. Some frameworks that gate on
`event.isTrusted` will ignore this — prefer `set_input_files` when a file
input exists."

**Parameters:**

| Name              | Type    | Req | Description |
|-------------------|---------|-----|-------------|
| `target_selector` | string  | yes | CSS selector of the drop zone (the element with the `drop` handler, or a wrapper — see §3.4 for resolution). |
| `local_file_path` | string  | yes | Absolute path on the MCP server host. `~` expanded, symlinks resolved, must be an existing regular file. Same validation contract as `set_input_files` (`resolveHostPaths`). |
| `mime_type`       | string  | no  | Override the `File` MIME type. Default: sniffed from extension via Go `mime.TypeByExtension`, falling back to `application/octet-stream`. |
| `tabId`           | number  | no  | Target tab; omit for active tab. Consistent with all other tools. |

Single file only in v1 (param is `local_file_path`, not an array). Multi-file
drop is a documented follow-up — the mechanism extends trivially
(`dataTransfer.items.add` per file) but the API and the host-token map get
more complex; keep v1 focused.

**Return shape** (JSON text result, matching the style of `setInputFiles`'s
`{ attached, multiple, name }`):

```json
{
  "dropped": true,
  "target_selector": ".dropzone",
  "file": { "name": "report.pdf", "size": 48213, "type": "application/pdf" },
  "events_dispatched": ["dragenter", "dragover", "drop"],
  "warnings": ["dropzone handler did not appear to react within 1500ms"]
}
```

`warnings` is best-effort (see §6). Errors return `mcp.NewToolResultError`
with a concrete message: file not found, no element for selector, fetch of
file bytes failed, page CSP blocked the loopback fetch.

## 3. Mechanism

### 3.1 Three options for moving bytes to the page

The hard part is not the `DragEvent` — it is delivering the file's bytes into
JavaScript running inside the target page. Three candidates:

**Option A — base64 inline through the existing WS channel.** The Go tool
reads the file, base64-encodes it, and ships it inside the `send()` params to
`background.js`, which forwards it to `content.js`, which does
`new File([Uint8Array.from(atob(b64)...)], name)`. No new server, no CORS.
*Cost:* the bytes traverse Go→WS→service-worker→`chrome.tabs.sendMessage`→
content script. base64 inflates payload ~33%. `chrome.runtime` messaging
serializes everything as JSON/structured-clone strings; a 20 MB file becomes a
~27 MB JSON string copied several times. The WS frame and the MV3 message port
both choke well before that. Fine for small files (< ~2 MB), bad as a general
mechanism.

**Option B — local HTTP host (chosen).** Reuse the project's established
loopback-HTTP pattern. The Go process serves the file bytes on a
`127.0.0.1` HTTP endpoint; the **content script** `fetch()`es that URL and
gets a `Blob` directly (`await resp.blob()`), then wraps it in a `File`.
Streaming, no base64 inflation, no MV3 message-size limit, browser-native
binary handling. This is exactly how the screenshot path already works in
reverse (`resizeNative` POSTs to `:18322`). *Cost:* the page must be allowed
to `fetch` a loopback URL — a CSP `connect-src` concern (see §5).

**Option C — the native helper at `:18322`.** The doc brief calls `:18322` a
"native helper (a screenshot resizer)". Important finding from the codebase:
**there is no Go HTTP server on `:18322` in this repo.** `resize.go` only
exposes `resizeImage`/`resizeBase64` as in-process functions; the daemon
(`ws.go`) serves WebSocket on `:18321` (`/`, `/relay`, `/version`) and nothing
on `:18322`. `background.js` *probes* `http://127.0.0.1:18322/health` and
falls back to `OffscreenCanvas` (`resizeLocal`) when it is absent — so the
`:18322` resizer is an **optional, separately-distributed** native binary, not
part of this build. We therefore should **not** bolt file-hosting onto a
helper that may not be running. Option B should host the file from the
**main Go MCP process**, which is always running when the tool is callable.

**Decision: Option B**, with **Option A as an automatic fallback for small
files** when the loopback fetch is blocked by page CSP (see §5/§6). This gives
a robust default and a graceful degradation path. Both produce the identical
`File` object inside the page; only the transport differs.

### 3.2 The local file-host endpoint

Add a small HTTP listener to the Go process. It binds **`127.0.0.1`** only
(loopback, never `0.0.0.0`), on a port chosen as follows:

- Preferred: a dedicated fixed port, e.g. `:18323` (sibling of the
  `:18321` WS port and the `:18322` resizer convention). Document it.
- The listener is started lazily on first `drag_drop_file` call and kept
  alive for the process lifetime (cheap; one `http.Server`).

It is **not** a static file server rooted at a directory. It serves files
**only by opaque single-use token**:

- `GET /file/{token}` → streams one file, then invalidates the token.
- `GET /health` → `200` for the extension's reachability probe (mirrors the
  resizer's `/health`).

Token lifecycle (in Go, a `sync.Map[token]fileGrant` guarded by a mutex):

1. `drag_drop_file` validates the path (`resolveHostPaths`-style), `os.Stat`s
   it, and mints a random 32-byte token (`crypto/rand`, hex/base64url).
2. It records `{absPath, mimeType, size, expiresAt: now+30s, used: false}`.
3. The token (not the path) is sent to the extension.
4. `GET /file/{token}`: look up; reject if missing, expired, or already used;
   mark used; `http.ServeContent` (sets `Content-Type`, `Content-Length`,
   supports range requests for free); delete the grant.
5. A background sweeper drops expired grants.

This keeps the filesystem-exposure surface to "exactly the one file the agent
named, for 30 seconds, fetchable once" — see §5.

### 3.3 Constructing the `File` and `DataTransfer` in the page

A content script (isolated world) **can** construct all of these — `File`,
`Blob`, `DataTransfer`, and `DragEvent` are standard DOM constructors, not
privileged APIs. The one subtlety: the `File`/`DataTransfer` must be created
in the **same realm as the page's drop handler** for `instanceof File` checks
in framework code to pass, and for the objects not to be rejected as
cross-realm. The content script's isolated world shares the DOM but is a
*separate JS realm* from the page (`world: 'MAIN'`). Two robust approaches:

- **Preferred: run the drop in the MAIN world.** `background.js` already has
  `executeJsMain` / `chrome.scripting.executeScript({ world: 'MAIN' })`, and
  `content.js` already has `injectScript` (injects a `<script>` element that
  runs in page context and posts the result back via `window.postMessage`).
  The fetch of the loopback URL and the `DragEvent` dispatch both happen in
  MAIN-world page context, so every object is page-realm.

- The `fetch` itself: doing it in MAIN world means the fetch is subject to the
  **page's CSP `connect-src`** (the real risk — §5). Doing the fetch in the
  *content script* (isolated world) bypasses page CSP for the fetch but then
  the `Blob` is isolated-realm. Cross-realm `Blob` → `File` is actually fine
  in practice (Blobs are transferable and structured-cloneable), but a
  `DataTransfer` built in the isolated realm and a `drop` handler reading it
  in the page realm is the fragile combination. **Resolution:** fetch in the
  content script (CSP-immune), transfer the `Blob` to a MAIN-world script via
  `postMessage` (structured clone — Blobs clone cleanly), and build the
  `File` + `DataTransfer` + `DragEvent` in MAIN world. This gets us both
  CSP-immunity for the transport *and* page-realm objects for the drop.

The page-context sequence (built as the injected MAIN-world script body):

```
// blob arrives via postMessage from the content script
const file = new File([blob], fileName, { type: mimeType, lastModified: Date.now() });
const dt = new DataTransfer();
dt.items.add(file);                       // dt.files now [file]

const target = document.querySelector(targetSelector);
const rect = target.getBoundingClientRect();
const cx = rect.left + rect.width / 2, cy = rect.top + rect.height / 2;

function fire(type) {
  const ev = new DragEvent(type, {
    bubbles: true, cancelable: true, composed: true,
    clientX: cx, clientY: cy,
  });
  // Some browsers ignore the dataTransfer ctor option; force it:
  Object.defineProperty(ev, 'dataTransfer', { value: dt });
  target.dispatchEvent(ev);
  return ev;
}

fire('dragenter');
fire('dragover');          // many libs require dragover before drop
const dropEv = fire('drop');
```

Notes grounded in DOM behavior:

- The `DragEvent` constructor's `dataTransfer` option is **not** honored by
  all engines; `Object.defineProperty` overriding the getter is the reliable
  way to attach the `DataTransfer`. (`new DataTransfer()` itself is
  well-supported in current Chrome and Firefox.)
- `dragenter` then `dragover` then `drop` mirrors the real browser sequence;
  `react-dropzone` and Dropzone.js track state across `dragenter`/`dragover`
  and will not process a bare `drop`. We also dispatch `dragleave` is *not*
  needed (and would cancel some libs); we skip it.
- `clientX/clientY` set to the drop zone's center so handlers that hit-test
  the coordinates accept it.
- Events are dispatched on the resolved target element; `bubbles: true` lets a
  handler bound on an ancestor (common with React's synthetic event
  delegation at the root) still receive them.

### 3.4 Selector resolution

`set_input_files` walks from a wrapper to a descendant `input[type=file]`.
For a drop zone there is no canonical descendant. Resolution rule:

1. `querySelector(target_selector)`. If no match → error.
2. Dispatch the drag events **on that element directly**. Do not walk. The
   drop handler is often on the styled container itself, and React delegates
   to the document root anyway, so dispatching on the named element with
   `bubbles: true` reaches handlers at or above it.
3. Reject a zero-size bounding box (`width < 1 && height < 1`) with a clear
   error — an invisible element is almost certainly the wrong target and the
   coordinates would be meaningless.

### 3.5 CDP-free upload to a real `<input type=file>`

The same `DataTransfer` trick **can** populate a genuine
`input[type=file]`, with one caveat. `HTMLInputElement.prototype.files` has a
setter (it accepts a `FileList`), and `DataTransfer.prototype.files` *is* a
`FileList`. So:

```
const input = document.querySelector(fileInputSelector);
input.files = dt.files;                       // works — assigns the FileList
input.dispatchEvent(new Event('input',  { bubbles: true }));
input.dispatchEvent(new Event('change', { bubbles: true }));
```

This is a fully CDP-free way to fill a file input — useful for Firefox and for
CDP-disabled Chrome. **Caveat:** the resulting `change` event has
`isTrusted === false`; a minority of sites guard upload on `isTrusted` and
will ignore it (same limitation as the drag path — §6). `set_input_files`
(CDP) does *not* have this caveat because CDP fires a trusted change.

**Recommendation:** keep `drag_drop_file` focused on drop zones, but have it
**auto-detect** when `target_selector` resolves to (or contains) an
`input[type=file]` and, in that case, use the `input.files = dt.files` path
instead of dispatching `DragEvent`s. The return shape reports which path ran
(`"method": "drop"` vs `"method": "input.files"`). This makes the one tool a
complete non-CDP upload fallback, with `set_input_files` remaining the
preferred (trusted) path whenever CDP is available.

## 4. Implementation across layers

### 4.1 Go tool — `tools_files.go`

- `registerFileTools` gains a third `addTool` for `drag_drop_file` with the
  schema in §2.
- `handleDragDropFile`:
  - Validate `target_selector` non-empty.
  - Reuse path validation: extract a single-path variant of
    `resolveHostPaths` (`resolveHostPath`) — `~` expand, require absolute,
    `EvalSymlinks`, `Stat`, reject directories. Factor the per-entry logic so
    both functions share it.
  - Determine MIME: param override, else `mime.TypeByExtension(filepath.Ext)`,
    else `application/octet-stream`.
  - Ensure the file-host server is running (`ensureFileHost()`, idempotent).
  - Mint a token; register the grant `{path, mime, size, expiry}`.
  - `send("drag_drop_file", { target_selector, tabId, fileName, mimeType,
    size, fileToken, fileHostPort })`. `intent` flows through as for every
    tool.
  - On the WS response, surface `dropped`/`warnings`/errors.

### 4.2 New file — `filehost.go`

- `ensureFileHost()` — `sync.Once`-guarded; `net.Listen("tcp",
  "127.0.0.1:18323")`; `http.Serve` with a mux for `/file/{token}` and
  `/health`. On port-in-use, fall back to an ephemeral port and remember it
  (the chosen port is passed to the extension in params, so a fixed port is
  not strictly required — but a fixed default keeps CSP allow-listing
  predictable; see §5).
- `mintFileGrant(absPath, mime string, size int64) string` — `crypto/rand`
  token, store in a mutex-guarded map with `expiresAt`.
- `/file/{token}` handler — validate token (exists, unexpired, unused), set
  `used`, `http.ServeContent`, delete grant. Also set
  `Access-Control-Allow-Origin` (see §5).
- Background goroutine: sweep expired grants every ~30s.
- Unit-testable without the browser: see §7.

### 4.3 Daemon — `ws.go`

No protocol change. `drag_drop_file` is just another action string flowing
through `send()`. The file bytes do **not** traverse the WS connection — they
go over the separate `:18323` HTTP channel — so existing frame-size and
timeout behavior is unaffected. The WS message carries only small JSON
metadata (token, name, mime, size, selector).

One consideration: the default `send` timeout. A drag-drop that fetches a
larger file in the page can take longer than a click. `handleDragDropFile`
should pass an explicit, generous timeout (the `send` variadic
`timeoutMs` parameter already supports this — e.g. 30 s).

### 4.4 `background.js`

- Add `'drag_drop_file'` to the action `switch` (near `set_input_files`):
  ```
  case 'drag_drop_file':
    return await toContent(params.tabId, 'drag_drop_file', {
      target_selector: params.target_selector,
      fileName: params.fileName, mimeType: params.mimeType,
      size: params.size, fileToken: params.fileToken,
      fileHostPort: params.fileHostPort,
    });
  ```
- This is a **content-script** action (like `extract_text`), routed via
  `toContent` — *not* a CDP action. That is the whole point: it never touches
  `chrome.debugger`, so it works when CDP is unavailable and in Firefox.
- Add `'drag_drop_file'` to `PAGE_ACTIONS_THAT_GATE_ON_CURSOR` so the on-page
  cursor animates to the drop zone before the drop fires — consistent with
  `set_input_files`, and gives the watching human a visible beat.
- `actionPalette` (in `content.js`): give `drag_drop_file` a tint — reuse the
  blue "input" family or add a distinct one.

### 4.5 `content.js`

- Add a `drag_drop_file` handler to the `handlers` map.
- The handler:
  1. Resolve `target_selector`; reject missing / zero-size element.
  2. `fetch('http://127.0.0.1:' + fileHostPort + '/file/' + fileToken)` **from
     the content script** (isolated world — immune to the page's CSP). On
     failure, the error message distinguishes "host unreachable" vs an HTTP
     error code.
  3. `const blob = await resp.blob();`
  4. Inject a MAIN-world script (extend the existing `injectScript`
     mechanism, or use a dedicated injected function) that receives the
     `blob` via `postMessage`, builds `File` + `DataTransfer`, detects
     input-vs-dropzone (§3.5), and dispatches the events / assigns
     `input.files`. The injected script `postMessage`s back a result:
     `{ dispatched: [...], reacted: bool }`.
  5. Best-effort reaction check: before dispatch, the injected script can
     hook the target's `drop` listener result, or simply observe DOM changes
     (a new filename label, a thumbnail) via a short `MutationObserver`
     window (~1500 ms). If nothing changed, add a `warning` — do not fail
     (some uploads are silent / async).
  6. Resolve `{ dropped: true, file: {...}, method, events_dispatched,
     warnings }`.
- `resolveTarget` in `content.js` (the overlay-cursor helper) gains a
  `drag_drop_file` branch returning the drop zone's bounding box so the
  cursor animates there.

### 4.6 Native helper

**Not used.** The `:18322` resizer is optional and out-of-repo (§3.1). The
file host is served by the always-present Go MCP process on its own
`:18323` listener. No dependency on the resizer being installed.

## 5. Security considerations

The new attack surface is a loopback HTTP server that reads the filesystem.
Mitigations:

- **Loopback bind only.** `127.0.0.1`, never `0.0.0.0`/`::`. Mirrors the WS
  daemon and resizer convention. Remote hosts cannot reach it.
- **No directory exposure / no path traversal.** `/file/{token}` resolves a
  token to a *pre-validated absolute path that was stored server-side*. The
  HTTP request never carries a path. There is no `..`, no glob, no
  user-controlled path in the URL. Even a malicious page on the tab can only
  request `/file/{token}` for tokens it does not know.
- **Unguessable, single-use, short-lived tokens.** 32 bytes of
  `crypto/rand`. One successful `GET` invalidates the token; expiry is ~30 s.
  A page script racing for the token has a tiny window and would still need
  to guess 256 bits.
- **The agent already controls which file.** `drag_drop_file` only ever
  exposes a file the MCP agent explicitly named — the same trust boundary as
  `set_input_files`, which already hands arbitrary host paths to the browser.
  This tool does not *widen* what the agent can do; it changes the transport.
- **CORS on the synthetic fetch.** The fetch originates from the *extension's*
  content script (isolated world), whose request `Origin` is the page's
  origin (content-script `fetch` uses the page origin). The `/file` handler
  should respond with `Access-Control-Allow-Origin: *` (or echo the request
  Origin) so the cross-origin fetch to `127.0.0.1:18323` is not blocked.
  Because the endpoint is loopback, single-use, and token-gated, `ACAO: *` is
  acceptable — there is nothing sensitive to protect with CORS that the token
  does not already protect.
- **Page CSP `connect-src`.** This is the real constraint. A content-script
  `fetch` is generally **not** subject to the page's CSP (content scripts run
  in a privileged isolated world) — this is why §3.5/§4.5 fetch in the
  *content script*, not MAIN world. The project's own Firefox build already
  ships a loopback CSP relaxation (`build.js`: `connect-src ... http://
  127.0.0.1:18322`) for the resizer; if any path needs a MAIN-world fetch,
  `18323` must be added there too. With the content-script-fetch design, no
  CSP change is needed for the *fetch*; the `DragEvent` dispatch and
  `new File()`/`new DataTransfer()` are not network operations and are never
  CSP-restricted.
- **The injected MAIN-world `<script>` and CSP `script-src`.** `injectScript`
  appends a `<script>` element; on pages with a strict `script-src` (no
  `'unsafe-inline'`, nonce-based) that injected script can be blocked. This is
  a pre-existing limitation of `adapt_script`/`inject_script`. Fallback: use
  `chrome.scripting.executeScript({ world: 'MAIN', func })` from
  `background.js`, which is **not** subject to page CSP. If `drag_drop_file`
  hits a CSP-strict page, route the MAIN-world step through
  `chrome.scripting.executeScript` rather than DOM `<script>` injection.
- **Token leak via logs.** Do not log the full token at info level; the URL
  with the token should not appear in the on-page intent toast or popup
  history.

## 6. Edge cases & failure modes

- **`isTrusted` gating.** Synthetic `DragEvent`s and the `input.files=` path
  both produce events with `isTrusted === false`. Sites that explicitly check
  `event.isTrusted` (a known anti-bot / security pattern — `content.js`
  already comments on this for `cdp_click`) will ignore the drop. This is an
  inherent, unfixable limitation of any non-CDP path. The tool description
  must say so, and the result `warnings` should flag "no observed reaction"
  so the agent can fall back to `set_input_files`/`intercept_file_chooser`.
- **Frameworks that ignore synthetic drops.** Even without `isTrusted`
  checks, some libraries require the exact event choreography: `dragover`
  must be `preventDefault`-ed by the page for `drop` to fire on a real
  browser, and some libs only arm `drop` after seeing `dragenter`. We
  dispatch `dragenter`→`dragover`→`drop`; if a library needs more (e.g.
  `dragover` repeated, or events on `document`), the drop silently no-ops →
  surfaced as a `warning`. React's synthetic event system reads
  `nativeEvent.dataTransfer`, which our `Object.defineProperty` override
  satisfies.
- **No reaction detected ≠ failure.** Uploads are often async (the widget
  POSTs in the background). The `MutationObserver` reaction check is
  advisory. We return `dropped: true` with a `warning`, never a hard error,
  when events dispatched cleanly but no DOM change was seen.
- **Large files.** Option B streams via `http.ServeContent`, so multi-hundred-
  MB files are mechanically fine for transport. Practical limits: the page
  holds the whole `Blob`/`File` in memory, and so does the daemon if Option A
  fallback triggers. Set a soft cap (e.g. warn above ~50 MB, reject above a
  hard limit like ~500 MB) to avoid OOMing the tab. Document it.
- **Element disappears between resolution and drop.** Re-query inside the
  injected script immediately before dispatch; error if gone.
- **Drop zone inside a cross-origin iframe.** The content script is injected
  with `all_frames: false` (`manifest.json`) — it only runs in the top
  frame. A drop zone in a child frame is unreachable. Document this; it is the
  same limitation `click`/`type_text` already have. (CDP `set_input_files`
  can reach frames; this is a reason to keep CDP as the primary path.)
- **File-host port already in use.** `ensureFileHost` falls back to an
  ephemeral port; the actual port is sent in params, so functionality is
  preserved (only a hypothetical MAIN-world CSP allow-list would need the
  fixed port).
- **Token expires before the page fetches it.** 30 s should be ample; if a
  page is extremely slow, the fetch 410s and the tool returns a clear
  "file grant expired, retry" error.
- **Firefox.** `DataTransfer`, `DragEvent`, `new File()` all work in Firefox;
  content-script `fetch` to loopback works. This tool is the *primary* upload
  path for the Firefox build, which has no `chrome.debugger`.

## 7. Testing approach

- **Go unit tests (`filehost_test.go`):** token mint → `GET /file/{token}`
  returns bytes + correct `Content-Type`; second `GET` 410s (single-use);
  expired token 410s; unknown token 404s; `/file/../etc/passwd`-style URLs
  cannot escape (there is no path in the URL — assert the handler ignores
  anything but a known token); `/health` 200; listener bound to `127.0.0.1`
  only. Reuse the `resize_test.go` style.
- **Go unit tests for path validation:** extend the existing
  `tools_handler_test.go` / `tools_custom_test.go` patterns — `resolveHostPath`
  rejects relative paths, expands `~`, resolves symlinks, rejects directories
  and missing files. MIME sniffing for common extensions.
- **Extension unit tests (`extension/__tests__`, vitest — the repo already has
  `vitest.config.js`):** a `drag_drop_file` content-handler test with a jsdom
  drop zone that records received `DragEvent`s; assert `dragenter`/`dragover`/
  `drop` order, that `event.dataTransfer.files[0]` is the expected `File`
  (name/size/type), and that the input-detection branch assigns
  `input.files`. Mock `fetch` to return a `Blob`.
- **Manual / integration matrix** (no automated browser harness in-repo):
  - A plain `ondrop` handler page — baseline.
  - A `react-dropzone` page — verifies the `dragenter`→`dragover`→`drop`
    choreography and React synthetic events.
  - A real `<input type=file>` with no drop zone — verifies the §3.5 branch.
  - A CSP-strict page (`script-src` nonce only) — verifies the
    `chrome.scripting.executeScript` MAIN-world fallback.
  - A page with an `isTrusted` guard — verifies we emit the `warning` rather
    than falsely reporting success.
  - Firefox build — verifies the whole non-CDP path end-to-end.
  - DevTools-open Chrome tab — verifies it works while CDP would be contended.
- **Regression:** confirm `set_input_files` and the screenshot/resizer path
  are unaffected (new listener on a new port, no shared state).

## 8. Effort estimate & risks

**Estimate: M (medium).** Roughly:

- Go file-host server + token lifecycle + tests — small-to-medium, but it is
  genuinely new surface (an HTTP server, not just a tool handler). ~1 day.
- `tools_files.go` tool registration + path/MIME plumbing — small, mirrors
  `set_input_files`. ~0.5 day.
- `background.js` routing — trivial (~10 lines).
- `content.js` handler + MAIN-world injection + reaction observer + overlay
  cursor branch — medium, the trickiest part is the realm/CSP handling.
  ~1–1.5 days.
- Extension vitest + Go tests — ~0.5 day.
- Cross-browser / framework manual matrix — ~0.5–1 day.

Total ≈ 4–5 days. It would be **S** if it were a pure content-script tool, but
the loopback file-host server, the realm-crossing `Blob` transfer, and the CSP
fallback push it to **M**.

**Risks:**

- **Realm / CSP interaction is the hardest part** and the most likely to need
  iteration. The fetch-in-isolated-world → `postMessage` `Blob` →
  build-File-in-MAIN-world design is chosen specifically to dodge both page
  CSP and cross-realm `instanceof` failures, but it must be validated against
  real strict-CSP sites; the `chrome.scripting.executeScript` fallback is the
  safety net.
- **Synthetic drops are inherently best-effort.** Some real-world widgets
  will not react no matter how faithfully we choreograph events
  (`isTrusted` guards, or libraries listening on `document`/`window` with
  extra state). The tool must be honestly documented as a *fallback*, and the
  `warnings` channel must make non-reaction visible so the agent escalates to
  CDP. This is a product risk, not a code bug.
- **New always-on loopback listener.** Adds a port and a (small) attack
  surface. Mitigated by loopback-only bind, token gating, single-use, short
  expiry — but it is a new thing to keep secure and to not leak tokens into
  logs/toasts.
- **`new DataTransfer()` / `DragEvent` constructor quirks** across Chrome and
  Firefox versions — the `Object.defineProperty` override for `dataTransfer`
  is the known-robust workaround; low residual risk.
- **Top-frame-only** (`all_frames: false`) means drop zones in iframes are out
  of reach — acceptable for v1, documented, and a reason CDP `set_input_files`
  remains the primary tool.
