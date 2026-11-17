# Changelog

All notable changes to this project are documented here. The latest release's
notes are also surfaced to agents via `check_for_updates` / `self_update`
(`whatsNew`) so they can invalidate stale assumptions after updating.

## [1.18.3](https://github.com/ikari-software/turboweb-mcp/compare/v1.18.2...v1.18.3) (2026-11-17)


### Bug Fixes

* **zen:** detect Zen by getBrowserInfo's zen field ([f39e32b](https://github.com/ikari-software/turboweb-mcp/commit/f39e32bde7529873c6b04b1b99b03f0482db7f54))

## [1.18.2](https://github.com/ikari-software/turboweb-mcp/compare/v1.18.1...v1.18.2) (2026-11-16)


### Bug Fixes

* **review:** keep the hello's Zen name through list_tabs; harden the hello and flash ([7f24d5e](https://github.com/ikari-software/turboweb-mcp/commit/7f24d5eae471a2d887b454032453403b6165643c))
* **screenshot:** flash after the capture, not during, so screenshots keep their contrast ([333f4ab](https://github.com/ikari-software/turboweb-mcp/commit/333f4abe30af8e960d3328e14d0567e697ef787c))
* screenshots keep their contrast; Zen is named Zen ([03e3e78](https://github.com/ikari-software/turboweb-mcp/commit/03e3e78fc3b0c45a6df8cd8c3b9fe5f9ef01bc6e))
* **zen:** identify Zen from runtime.getBrowserInfo in the hello ([89a91d7](https://github.com/ikari-software/turboweb-mcp/commit/89a91d750218794226bbb2e1c9a769662ca2e429))

## [1.18.1](https://github.com/ikari-software/turboweb-mcp/compare/v1.18.0...v1.18.1) (2026-11-16)


### Bug Fixes

* **screenshot:** decode the capture without fetch() so Firefox screenshots work ([83a0223](https://github.com/ikari-software/turboweb-mcp/commit/83a0223617543b2d6f750c046fe85b5a8efbc1a4))
* **screenshot:** decode the capture without fetch() so Firefox screenshots work ([b53fde8](https://github.com/ikari-software/turboweb-mcp/commit/b53fde818919139a41d199e78906f1e4d5a9bc64))

## [1.18.0](https://github.com/ikari-software/turboweb-mcp/compare/v1.17.0...v1.18.0) (2026-11-16)


### Features

* **self_update:** offer the checksum-verified release .xpi to Firefox and Zen installs that lag behind ([e5f8ba9](https://github.com/ikari-software/turboweb-mcp/commit/e5f8ba98c6a62426c8250a310ed86660359aed80))
* **self_update:** offer the checksum-verified release .xpi to Firefox and Zen installs that lag behind ([3f82f85](https://github.com/ikari-software/turboweb-mcp/commit/3f82f8519fb5ed5e08e1abd9b5bb5c27c77cf011))
* **self_update:** offer the checksum-verified release .xpi to Firefox and Zen installs that lag behind ([e20ac5a](https://github.com/ikari-software/turboweb-mcp/commit/e20ac5ab172c8f319309086ea771c87ae906bc19))


### Bug Fixes

* **ci:** mint Chrome Web Store token without the IAM Credentials API ([3f2966c](https://github.com/ikari-software/turboweb-mcp/commit/3f2966c4e0869524e568c1a98e16528400f94bad))
* **ci:** mint Chrome Web Store token without the IAM Credentials API ([e8de8f7](https://github.com/ikari-software/turboweb-mcp/commit/e8de8f74eea312fb908faac3c336f02844e73227))
* **self_update:** make the .xpi offer work from the CLI and tell agents what happened ([e5f8ba9](https://github.com/ikari-software/turboweb-mcp/commit/e5f8ba98c6a62426c8250a310ed86660359aed80))
* **self_update:** make the .xpi offer work from the CLI and tell agents what happened ([3f82f85](https://github.com/ikari-software/turboweb-mcp/commit/3f82f8519fb5ed5e08e1abd9b5bb5c27c77cf011))
* **self_update:** make the .xpi offer work from the CLI and tell agents what happened ([e20ac5a](https://github.com/ikari-software/turboweb-mcp/commit/e20ac5ab172c8f319309086ea771c87ae906bc19))

## [1.17.0](https://github.com/ikari-software/turboweb-mcp/compare/v1.16.0...v1.17.0) (2026-11-16)


### Features

* group_tabs and ungroup_tabs, and group names in list_tabs ([5adcf19](https://github.com/ikari-software/turboweb-mcp/commit/5adcf194d68a778a07d6e487c0a2ad39deeeac1a))
* group_tabs and ungroup_tabs, and group names in list_tabs ([168d2e3](https://github.com/ikari-software/turboweb-mcp/commit/168d2e3d088a2ff8c297b606d79307e1f6a7f323))
* hide overlay text chrome in screenshots (+ dep upgrades) ([#42](https://github.com/ikari-software/turboweb-mcp/issues/42)) ([004f936](https://github.com/ikari-software/turboweb-mcp/commit/004f936cbdf88c1f2526c0acdd27f64cca66c54f))

## [1.16.0](https://github.com/ikari-software/turboweb-mcp/compare/v1.15.0...v1.16.0) (2026-11-15)


### Features

* ask the browser's user before an agent may connect ([#39](https://github.com/ikari-software/turboweb-mcp/issues/39)) ([372486e](https://github.com/ikari-software/turboweb-mcp/commit/372486ef2cf8d202737b1dff9a177eeffb42d9db))
* hide overlay text chrome in screenshots ([ab06508](https://github.com/ikari-software/turboweb-mcp/commit/ab06508ac314c12e8a8a9b5d786e83fac12bbbaf))
* submit the extension to the Chrome Web Store from CI ([958c402](https://github.com/ikari-software/turboweb-mcp/commit/958c40276333ca9d9323996e5dd2da6ecedd383f))


### Bug Fixes

* let an agent see, and a headless host skip, the approval gate ([#41](https://github.com/ikari-software/turboweb-mcp/issues/41)) ([0678e6d](https://github.com/ikari-software/turboweb-mcp/commit/0678e6d2fb2ae847546b9e25562d750d17202f2d))
* review follow-ups for tab management (stale tab ids, BiDi-only list_tabs, move_tabs at the pinned boundary) ([#37](https://github.com/ikari-software/turboweb-mcp/issues/37)) ([6197120](https://github.com/ikari-software/turboweb-mcp/commit/6197120a347289b2118d9c1b6402215105da71df))
* warn that a debug port changes a Firefox profile's settings for good ([#40](https://github.com/ikari-software/turboweb-mcp/issues/40)) ([369d660](https://github.com/ikari-software/turboweb-mcp/commit/369d6600f5d6b41e6d86d420025249bb62e94988))

## [1.15.0](https://github.com/ikari-software/turboweb-mcp/compare/v1.14.0...v1.15.0) (2026-11-11)


### Features

* tab management tools, mandatory tabId, and errors that say what to do next ([#35](https://github.com/ikari-software/turboweb-mcp/issues/35)) ([e81c056](https://github.com/ikari-software/turboweb-mcp/commit/e81c056342182a2108ed4dca39eb6feb6c622b4e))

## [1.14.0](https://github.com/ikari-software/turboweb-mcp/compare/v1.13.0...v1.14.0) (2026-11-03)


### Features

* capture_page saves the tab's rendered DOM to a local file ([#32](https://github.com/ikari-software/turboweb-mcp/issues/32)) ([5ccf7d9](https://github.com/ikari-software/turboweb-mcp/commit/5ccf7d9751d934ac052b153bbc66f91b44d476f5))


### Bug Fixes

* browser-prefixed tab ids so Firefox and Zen tabs don't collide ([#34](https://github.com/ikari-software/turboweb-mcp/issues/34)) ([e46dd97](https://github.com/ikari-software/turboweb-mcp/commit/e46dd97344b14966ae40d9140852041e96940061))

## [1.13.0](https://github.com/ikari-software/turboweb-mcp/compare/v1.12.1...v1.13.0) (2026-09-29)


### Features

* agents introduce themselves; fix host detection and popup labels ([#28](https://github.com/ikari-software/turboweb-mcp/issues/28)) ([de93696](https://github.com/ikari-software/turboweb-mcp/commit/de93696f0fd014ceadc8f03a815480e870bb366a))

## 1.12.1

### `self_update` extension hot-swap from an installed binary
- Fixed `self_update` not reloading the loaded browser extension when the server
  runs from an install location away from the repo (e.g. `/opt/homebrew/bin`): it
  couldn't locate the unpacked extension dir to swap. **`TURBOWEB_EXTENSION_DIR`
  now points at the extension SOURCE root** (the dir holding `dist/chrome` and
  `dist/firefox`) and is honored in `findExtensionDir`, so the one override
  resolves **both** the Chrome/Arc and Firefox dist dirs — previously it fed only
  the Chrome path. Set it in the MCP server env and load the unpacked extension
  from that root's `dist/<browser>` to have every connected browser hot-swap on
  update.

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
