# Changelog

All notable changes to this project are documented here. The latest release's
notes are also surfaced to agents via `check_for_updates` / `self_update`
(`whatsNew`) so they can invalidate stale assumptions after updating.

## 1.12.0

### Trusted input into cross-origin frames on Chrome (no BiDi)
- **`cdp_click` / `cdp_type` / `cdp_key` / `cdp_scroll` now reach same- AND
  cross-origin frames on Chrome without BiDi** (previously BiDi/Firefox only).
  The element is resolved inside the frame via the `all_frames` content script
  and trusted input is dispatched on the top target, so Chrome's cross-process
  input router delivers it to the OOPIF. Keys follow focus (`cdp_type` clicks the
  field first); `clear=true` selects all via **triple-click** (the Meta/Ctrl+A
  chord does not cross the OOPIF process boundary); off-screen frame targets are
  **scrolled into view** first (across cross-origin ancestors). Live-validated
  same-origin, single-hop, and two-hop cross-origin.

### Sticky frame context
- **`frame_set(framePath)` / `frame_context`** — scope every subsequent
  frame-aware call (DOM reads and `cdp_*`) to one frame until cleared, instead of
  repeating the `frame` arg. Injected at the `addTool` chokepoint (a per-call
  `frame` still wins); `navigate` is excluded on purpose.

### Cross-origin frame targeting hardening
- `resolveFrameContext` now maps an iframe to its BiDi child browsing context by
  **unique URL → unique origin → count-gated document-order index**, and **errors
  loudly** on ambiguity instead of silently dispatching trusted input into the
  wrong cross-origin frame.

### Maintainability, tests, and fixes
- Cross-iframe cleanups (shared `queryFrameEl` validator, reciprocal cross-file
  offset comments, dropped redundant `frameId`), and expanded frame test coverage.
- Fixes from a multi-agent code review: restored the `navigate` empty-`url` guard
  (mcp-go does not enforce `Required` at the call layer), excluded `navigate`
  from sticky-frame injection, refused `cdp_type clear` in a frame without a
  selector, and stopped swallowing non-"not found" scroll failures.
- Agent guidance now documents `frame_set`/`frame_context` and the no-BiDi
  cross-origin `cdp_*` capability.

## 1.11.0

### Cross-origin frames
- Content script now runs in **every frame** (`all_frames`), so DOM reads and
  synthetic interaction (`extract_text`, `find_text`, `query_elements`,
  `click`, `type_text`, `fill_input`, `scroll`) reach **cross-origin** iframes —
  not just same-origin ones. Frames are addressed by a `>`-separated CSS
  selector path; coordinates stay top-viewport-relative.

### Trusted input (`cdp_*`)
- `cdp_key` gains **modifier chords** (e.g. Meta/Control+A for select-all) and
  proper key descriptors, so **Backspace/Delete actually delete**.
- `cdp_type clear=true` now works on the Chrome backend (previously a no-op).
- `cdp_click` gains `clickCount` — double-click (select word) / triple-click
  (select all field text).
- `cdp_type` types at a **human cadence by default** (`wpm`, default 110;
  `wpm=0` for instant) with jitter and longer pauses after spaces/punctuation.
- `type_text` / `fill_input` dispatch a real `InputEvent` so controlled
  components that gate on `inputType`/`data` update correctly.

### New tools
- **`connect_bidi(port)`** — attach WebDriver BiDi to a browser you launched
  yourself with `--remote-debugging-port` (your real profile), instead of
  `launch_browser`'s throwaway profile. Useful on Firefox/Zen, where trusted
  input has no other route.

### Agent ergonomics
- `check_for_updates` / `self_update` now return **`whatsNew`** (release notes)
  so agents can learn what changed and invalidate stale memory.
- `self_update` swaps in the unpacked extension **in place for every connected
  browser** — load-unpacked Chrome **and** temporary-add-on Firefox — and
  reloads each, in addition to replacing the MCP binary. (Chrome Web Store /
  Firefox AMO installs keep auto-updating via the store / `update_url`.)
- Tool descriptions and the agent-rules prompt steer toward **selectors over
  coordinates** and document cross-origin frame support.

## 1.10.0
- Cross-iframe support across DOM tools; single-frame navigation that preserves
  the parent frameset; Zen BiDi launch detection fix; single-source `VERSION`.
