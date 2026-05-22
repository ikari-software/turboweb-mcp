# Design: `screenshot_diff` — prove an action changed the page

Status: Draft
Author: design doc
Scope: new MCP tool + supporting changes across Go, extension, native helper

---

## 1. Problem & motivation

After an agent performs an action (`click`, `type_text`, `navigate`, `cdp_*`),
its very next question is almost always *"did anything actually change?"*. Today
the only way to answer that is to take a second `screenshot` (or
`turbo_snapshot`) and ship a full JPEG to the model so it can eyeball the
before/after pair.

That is expensive and unreliable:

- **Token cost.** Each `screenshot` is a JPEG scaled to `maxWidth` 1280 at
  quality 70 — typically 30–120 KB, i.e. thousands of vision tokens. Verifying
  an action doubles that, and agents verify constantly.
- **Latency.** The extension screenshot path (`background.js:screenshot`) must
  `chrome.windows.update({focused:true})`, `chrome.tabs.update({active:true})`,
  wait `SCREENSHOT_FOCUS_DELAY_MS` (150 ms), then `captureVisibleTab`, then
  resize. Two of those back-to-back is ~0.5–1 s plus image transfer.
- **The model is a bad differ.** Asking a vision model "spot the difference"
  between two large images is error-prone — it misses small toasts, badge count
  changes, and subtle disabled-state flips, and it hallucinates changes that are
  just JPEG noise.

What the agent actually needs is a cheap, deterministic verdict: **changed
yes/no, where, how much, and only if useful a tiny thumbnail**. That is a job
for pixel/DOM comparison done in code, not for the model.

`screenshot_diff(before, after)` provides exactly that.

---

## 2. Proposed MCP tool API

Registered in `tools_browser.go` alongside `screenshot` / `turbo_snapshot`.

### Tool name

`screenshot_diff`

### Two usage modes

The tool supports both an **explicit** mode (agent already has two captures)
and a far more useful **capture-bracketed** mode (the tool captures both
states itself around the agent's verification window).

In practice agents will mostly use a third, even cheaper variant: a *baseline
token*. Rather than carrying raw image bytes through the model context, the
agent gets an opaque `baseline_id` it can diff against later.

### Params

```
screenshot_diff:
  intent        string   (required — per project convention, narrated on overlay)
  tabId         number   (optional — defaults to active tab)

  # one of the following baseline sources, in priority order:
  baseline_id   string   (optional — token from a previous screenshot_diff
                          or screenshot_baseline call; server-cached image)
  before        string   (optional — base64 JPEG/PNG, explicit before image)

  # what to compare it against:
  after         string   (optional — base64 JPEG/PNG; if omitted the tool
                          captures the current page state itself)

  # tuning:
  threshold     number   (optional — per-pixel perceptual delta 0..1 above
                          which a pixel counts as "changed"; default 0.10)
  min_score     number   (optional — similarity 0..1 below which verdict is
                          "changed"; default 0.997, i.e. 0.3% of pixels)
  ignore        string[] (optional — CSS selectors whose bounding boxes are
                          masked out before diffing, e.g. ["#clock",".ad"])
  include_thumb boolean  (optional — return a diff thumbnail; default false)
  dom_signal    boolean  (optional — also return the MutationObserver verdict;
                          default true, it is nearly free)
```

If neither `baseline_id` nor `before` is supplied, the call instead **mints a
baseline**: it captures the current state, caches it, and returns a
`baseline_id` with no diff. This is the "mark before the action" call.

Typical agent flow:

```
screenshot_diff(intent:"mark page state before clicking Save")   -> {baseline_id:"b_8f3a"}
click(...)
screenshot_diff(intent:"verify the Save click changed the page",
                baseline_id:"b_8f3a")                            -> verdict
```

### Return shape — the cheapest useful payload

The default return is **pure JSON, no image** — a few hundred bytes:

```json
{
  "changed": true,
  "score": 0.948,
  "changed_fraction": 0.052,
  "regions": [
    { "x": 1040, "y": 12, "w": 220, "h": 64, "area_frac": 0.011, "label": "top-right" },
    { "x": 320, "y": 600, "w": 540, "h": 180, "area_frac": 0.041, "label": "center" }
  ],
  "region_count": 2,
  "dom": { "mutations": 7, "added": 1, "removed": 0, "attrs": 3, "text": 2,
           "largest_subtree": "div.modal" },
  "viewport": { "w": 1280, "h": 800 },
  "scrolled": false,
  "baseline_id": "b_9c21",
  "thumb_omitted": true
}
```

- `changed` — the single boolean the agent usually wants. `true` when
  `score < min_score` **or** the DOM signal reports structural mutations.
- `score` — similarity in `[0,1]`, `1.0` == identical. Lets the agent reason
  about *how much* changed (a caret blink vs a navigation).
- `changed_fraction` — fraction of compared pixels that exceeded `threshold`.
- `regions` — up to ~8 merged bounding boxes of changed areas, in *after*-image
  coordinates, largest first. `label` is a coarse 3×3 grid name purely as a
  human hint. This is what replaces "ship the whole image".
- `dom` — the MutationObserver summary (section 5). Cheap structural evidence;
  present when `dom_signal` is true and a content script is attached.
- `scrolled` — true when the diff detected a global vertical shift (section 7);
  signals the agent that regions are unreliable and it should re-snapshot.
- `baseline_id` — the *after* image is automatically cached as the next
  baseline, so chained verifications never re-upload.
- When `include_thumb:true`, an additional MCP `ImageContent` block is appended:
  a small (default 320 px wide) JPEG of the *after* image with changed regions
  outlined, OR a side-by-side crop of just the largest changed region. This is
  opt-in precisely because the point of the tool is to *avoid* shipping images.

The returned `mcp.CallToolResult` therefore normally has a single
`TextContent`; with `include_thumb` it has `TextContent` + `ImageContent`,
mirroring how `turbo_snapshot` already returns image + text.

---

## 3. Where the diff is computed

Three candidate locations exist in the architecture:

| Location | Has image libs? | Has page/DOM? | Cost to add |
|---|---|---|---|
| Go MCP/daemon (`resize.go`) | yes — `image`, `image/jpeg`, `nfnt/resize` already vendored | no | low |
| Native helper (`:18322`) | yes — it already does resize work | no | medium (separate process/repo) |
| Extension (`background.js`/`content.js`) | `OffscreenCanvas` only | yes (content.js) | high, and slow |

### Recommendation: **compute the pixel diff in Go**, in a new `diff.go`.

Rationale:

1. **The decode/compare toolchain is already there.** `resize.go` already
   decodes JPEG/PNG via `image.Decode` and re-encodes JPEG. A diff is the same
   shape of work: decode two images to `image.Image`, walk pixels, encode an
   optional thumbnail. No new dependency — `image` + a small perceptual-delta
   function cover it. `bufPool` can be reused for thumbnail encoding.
2. **Go already holds the images.** When the tool captures `after` itself, the
   bytes arrive in the Go process anyway (via `send("screenshot", …)` →
   extension → daemon → relay). Diffing there avoids a second network hop.
3. **The baseline cache belongs server-side.** A `baseline_id → []byte` LRU
   cache (bounded, ~16 entries, TTL ~5 min) lives naturally in the Go process
   and is what makes chained verification cheap. The extension is a service
   worker and gets killed/restarted — it is a bad cache owner.
4. **CPU cost is trivial.** A 1280×800 image is ~1 M pixels; a thresholded
   per-pixel compare is well under 10 ms in Go — far cheaper than the
   ~500 ms screenshot capture it follows.

The native helper (`:18322`) is **not** chosen as the primary site: it is an
optional, separately-deployed sidecar (background.js treats it as
maybe-absent — `checkNative()` caches availability and the code always has an
`OffscreenCanvas` fallback). Putting core diff logic behind an optional process
would make the tool flaky. *However*, if the native helper is present it is the
ideal place to run an **optional heavier perceptual diff** (SSIM, blur-based
denoise) without bloating the Go binary — see section 6 for that extension
point. Baseline behavior must work with Go alone.

The extension is rejected for the pixel diff: `OffscreenCanvas` pixel loops in
a service worker are slow, the worker can be evicted mid-compare, and it would
duplicate logic already trivial in Go. The extension *does* own one piece — the
DOM-mutation signal (section 5) — because only it has the live DOM.

---

## 4. Diff algorithm

### 4.1 Alignment & preprocessing

Both images are decoded and normalized to a common size. Browser captures at
the same `maxWidth` should already match, but a `navigate` can change viewport
height. Rule:

- If dimensions differ, scale the smaller to the larger's width (reuse
  `nfnt/resize`), then compare the overlapping top-aligned region; report the
  size change explicitly and set `changed:true`.
- Apply `ignore` selector masks: the *content script* resolves each selector to
  bounding boxes (it has the DOM) and returns them with the capture; Go zeroes
  those rectangles in both images before comparing.

### 4.2 Exact vs perceptual

**Perceptual, not exact.** An exact `==` byte compare is useless here — JPEG is
lossy, so two captures of a visually identical page differ in thousands of
pixels. The compare is per-pixel in a perceptual-ish space:

- Convert each pixel to luma + chroma (`Y`, plus down-weighted `Cb`/`Cr`), or
  simply compare `RGB` with luma-weighted distance
  `d = 0.6·dR + 0.7·dG + 0.3·dB` normalized to `[0,1]`.
- A pixel "changed" iff `d > threshold` (default `0.10`).
- This tolerates JPEG quantization noise (`d` ~0.01–0.03) while catching real
  changes (text appearing, color flips: `d` ~0.3+).

This is the pixelmatch algorithm, reimplemented minimally in Go (~80 lines, no
dependency). A YIQ-based delta as pixelmatch uses is acceptable and slightly
better at antialiasing tolerance.

### 4.3 Noise suppression

Sources of false positives and how each is handled:

- **JPEG/antialiasing noise** — handled by the `threshold` floor above. An
  optional antialias check (a changed pixel surrounded by neighbours that are
  bright/dark extrema of one of the two images is treated as AA, not a change)
  can be ported from pixelmatch if the floor alone proves too noisy.
- **Caret blink** — a text caret is a 1–2 px wide, tall, fully on/off
  rectangle. Two mitigations: (a) a connected-changed-region narrower than
  ~3 px and at least ~8× taller than wide is classified as a caret and dropped;
  (b) the agent can `ignore` the focused input's selector. Caret blink also
  produces *zero* DOM mutations, so the DOM signal naturally ignores it — a
  reason to weight DOM evidence (section 5).
- **Mouse cursor / TurboWeb overlay** — the project draws its own animated
  cursor and intent toasts/flash (content.js, the `flash` action for
  `screenshot`). These move between captures and would diff as noise. The
  capture step must quiesce the overlay: extend the existing screenshot path so
  `screenshot_diff` captures *with overlay animations suppressed* (the content
  script already gates overlay rendering; add a "freeze overlay for capture"
  mode, or capture in a brief window after animations settle). The OS cursor is
  generally not in `captureVisibleTab` output, but if present it is small and
  filtered by `min_score`.
- **Scrollbar** — a fixed ~15 px right-edge strip; its thumb moves when content
  height changes. Mask the rightmost scrollbar-width column before diffing
  (cheap, constant). On macOS overlay scrollbars are usually absent anyway.
- **Sub-pixel reflow** — text reflowing by 1 px lights up large regions for a
  trivial change. The `threshold` floor plus requiring a connected region to
  exceed a minimum area (`min_score` / `region` area floor) suppresses most.

### 4.4 Region detection

After the per-pixel changed-mask is built:

1. Run a fast connected-components / flood-fill pass over the changed mask
   (downsample the mask to ~1/4 resolution first for speed — region boxes do
   not need pixel precision).
2. Compute a bounding box per component; discard boxes below an area floor
   (e.g. < 0.05% of viewport) as noise.
3. Merge boxes that overlap or are within a small gap, cap at ~8, sort by area
   descending.
4. `score = 1 - changed_fraction` where `changed_fraction` counts mask pixels
   over total compared pixels. `changed = score < min_score || dom.mutations>0`.

---

## 5. DOM-mutation signal — the cheap complement

A pixel diff needs two screenshots (~0.5–1 s + transfer). A `MutationObserver`
in `content.js` answers "did the DOM change?" for **free** — no capture, no
image, a handful of bytes. For most actions (a click that opens a menu, a type
that fills a field, an XHR that injects a row) the DOM signal alone is a
sufficient verdict, and pixel diff becomes a confirm-only fallback.

### Mechanism (content.js)

- On content-script init, install one persistent `MutationObserver` on
  `document.documentElement` with `{childList, subtree, attributes,
  characterData}`.
- Maintain a small rolling counter object, reset on demand:
  `{ mutations, added, removed, attrs, text, firstAt, lastAt, largestSubtree }`.
  `largestSubtree` is a coarse CSS-path label of the highest-impact mutated
  node, useful as a "what changed" hint.
- Expose two content-script actions in the existing dispatch table
  (the `get_interactive_map: () => …` style map near content.js:1834):
  - `dom_mutations_mark` — snapshot+reset the counter, return a token.
  - `dom_mutations_since` — return counts accumulated since a given mark.
- Ignore mutations originating from TurboWeb's own overlay nodes (filter by the
  overlay's root element / a data attribute) so the cursor and toasts do not
  inflate counts — the same overlay nodes masked in the pixel diff.

### How the tool uses it

- The **baseline-minting** call (`screenshot_diff` with no baseline) also issues
  `dom_mutations_mark` and stores the token next to the cached image.
- The **verify** call issues `dom_mutations_since` and folds the result into the
  `dom` field of the return.
- `changed` is `true` if *either* the pixel score crosses `min_score` *or*
  `dom.mutations > 0`. They are complementary: DOM catches off-screen and
  visually-subtle changes (a hidden form field, an `aria-disabled` flip) that
  pixels miss; pixels catch `<canvas>`/`<video>`/CSS-only changes that produce
  no mutations.
- Optimization: if `dom_signal` is on and `dom.mutations == 0` **and** the
  action was a pure click with no navigation, the tool may *skip the after
  capture entirely* and return `changed:false` with `score:1.0,
  pixel_skipped:true`. This is the cheapest possible path — zero images — and is
  the common case for "did this no-op?". Behind a `fast` flag, defaulting on.

DOM-only mode caveat: cross-origin iframes and closed shadow roots are not
observable. When the tab has such content the tool notes
`dom.partial:true` and does not let a zero DOM count suppress the pixel diff.

---

## 6. Implementation across layers

### Go MCP tool — `tools_browser.go` + new `diff.go`

- `tools_browser.go`: add `registerBrowserTools` entry for `screenshot_diff`
  with the params from section 2, and `handleScreenshotDiff`.
- `handleScreenshotDiff` logic:
  1. Resolve baseline: `baseline_id` → cache lookup; else `before` arg; else
     mint-baseline path (capture now, cache, return `baseline_id` only).
  2. Resolve `after`: explicit arg, else capture via the existing path
     (`send("screenshot", …)`, or BiDi via `bidiScreenshot` like
     `handleScreenshot` does — prefer BiDi, no focus needed).
  3. Fetch `ignore` masks + DOM signal: `send("screenshot_diff_meta", …)` to the
     content script returning `{masks:[…], dom:{…}}` (one round trip).
  4. Call `diff.go` to compute mask, score, regions, optional thumbnail.
  5. Cache `after` as a fresh baseline; assemble JSON via `textResult` (and
     append `mcp.NewImageContent` when `include_thumb`).
- `diff.go`: `func diffImages(before, after []byte, opts DiffOpts)
  (DiffResult, error)` — decode (reuse `image.Decode`), align, mask, perceptual
  per-pixel compare, connected-components, optional annotated thumbnail
  (reuse `bufPool` + `jpeg.Encode`). Pure, no I/O — directly unit-testable.
- Baseline cache: a small mutex-guarded LRU (`map[string]baselineEntry` +
  insertion order, ~16 cap, 5-min TTL). `baseline_id` = short random hex.
- Relay note: `screenshot` already routes through `send()` to the daemon in
  relay mode; `screenshot_diff` inherits that. The baseline cache lives in
  whichever process runs the tool handler (the relay/MCP process), which is
  fine since the same process serves the chained calls.

### Daemon — `ws.go`

- No new command type for the pixel diff (Go does it). Add pass-through routing
  for the new content-script actions `screenshot_diff_meta` /
  `dom_mutations_mark` / `dom_mutations_since` — these are ordinary
  content-bridge commands and need only be allowed through the existing
  dispatch, no special handling.

### Native helper (`:18322`) — optional enhancement only

- Core feature ships **without** touching the helper. As an opt-in upgrade, add
  a `/diff` endpoint (`POST {before, after, threshold}` → `{score, regions,
  thumb}`) that runs a heavier **SSIM**-based perceptual diff. `diff.go`
  probes the helper exactly as `background.js:checkNative()` does (200 ms
  health check, cached, recheck interval) and uses it when present, else falls
  back to the built-in Go pixelmatch. Pure performance/quality upgrade, never
  required.

### `background.js`

- `dispatch` already forwards unknown content-script actions via `toContent`.
  Confirm `screenshot_diff_meta`, `dom_mutations_mark`, `dom_mutations_since`
  reach the content script (add to the bridge list if dispatch is allowlisted).
- Optionally add a `captureForDiff` wrapper around `screenshot()` that asks the
  content script to freeze overlay animations for the capture window, then
  restores them — keeps the cursor/toast out of the diff (section 4.3).
- No new screenshot logic — `screenshot_diff` reuses `screenshot()`.

### `content.js`

- Install the persistent `MutationObserver` at init (section 5).
- Add dispatch entries: `dom_mutations_mark`, `dom_mutations_since`,
  `screenshot_diff_meta` (the latter resolves `ignore` selectors to viewport
  bounding boxes and bundles the current DOM-mutation summary).
- Add an overlay-freeze hook used by `captureForDiff`.
- Filter the observer against TurboWeb's own overlay subtree so the project's
  cursor/toast/flash never count as page mutations.

---

## 7. Edge cases

- **Animations / spinners / GIFs / carousels.** Continuously animating pixels
  diff as permanent change. Mitigations: (a) the DOM signal stays quiet for
  pure CSS animation, so `dom.mutations==0` + a small steady changed region is
  reported as `likely_animation:true` rather than a hard `changed`; (b) capture
  *after* a short settle delay; (c) agent can `ignore` the animated selector.
- **`<video>` / `<canvas>`.** Always differ frame-to-frame and produce no DOM
  mutations. Detect known video/canvas regions via the content script's
  `screenshot_diff_meta` and auto-mask them unless the agent opts in. Note them
  in the result so the agent knows a region was excluded.
- **Lazy-loaded images / late content.** Content arriving between captures is a
  *real* change and should be reported — but it can race the verify capture.
  The DOM signal disambiguates: image-load mutations show as `added/attrs`.
  Recommend (in the tool description) capturing `after` once the page is idle.
- **Viewport scroll.** If the action scrolled the page, nearly every pixel
  shifts and the diff is meaningless. Detect a dominant global vertical shift
  (cross-correlate a few horizontal strips, or compare `scrollY` reported by
  the content script's `viewport()` before/after — content.js already exposes
  scroll position). When detected: set `scrolled:true`, skip region detection,
  and tell the agent to re-baseline. Optionally diff after re-aligning by the
  detected offset.
- **Navigation / full page replace.** Trivially `changed:true`; URL change is
  itself the verdict — short-circuit before diffing if `before`/`after` URLs
  differ (the tool can read both from the capture metadata).
- **Theme / DPR / size change.** Different capture dimensions → handled by
  section 4.1; reported as a change.
- **Empty/garbage baseline (expired `baseline_id`).** Return a clear error
  telling the agent to mint a new baseline; do not silently treat as changed.

---

## 8. Testing approach

- **`diff.go` unit tests (`diff_test.go`)** — the bulk of coverage, pure
  functions, mirrors `resize_test.go` style:
  - identical image pair → `score == 1.0`, `changed == false`, no regions.
  - synthetic pair with a known colored rectangle painted in → one region whose
    bbox matches within a tolerance; `changed_fraction` ≈ rectangle area.
  - JPEG-recompression noise pair (same image encoded twice at q70) →
    `changed == false` (validates the `threshold` floor).
  - a 1×N tall thin stripe (caret) → dropped by the caret heuristic.
  - vertically shifted pair → `scrolled == true`, regions skipped.
  - mismatched dimensions → handled, change reported, no panic.
  - region merging: two adjacent rects → one merged box.
- **Baseline cache tests** — mint → lookup → expiry/TTL → LRU eviction →
  expired-id error path.
- **Tool handler tests (`tools_handler_test.go`)** — extend the existing
  harness: stub the `send()` transport to return canned screenshot + meta
  payloads, assert the JSON return shape (`changed`, `score`, `regions`,
  `dom`), and assert `include_thumb` toggles the `ImageContent` block.
- **content.js MutationObserver tests** — under `extension/__tests__`
  (vitest is already configured): drive DOM mutations in jsdom, assert
  `dom_mutations_mark`/`since` counts and that overlay-subtree mutations are
  filtered out.
- **Manual / integration** — a fixture page with: a toggle button (DOM change),
  a CSS-only hover animation (no DOM change), a `<video>`, and a blinking
  caret; verify each produces the expected verdict end-to-end against a real
  browser.
- **Golden thumbnails** — keep one or two committed expected thumbnails for the
  annotated-region output, compared with the same perceptual diff (dogfooding).

---

## 9. Effort estimate & risks

**Overall: M (medium).** Roughly:

- `diff.go` perceptual pixelmatch + connected components — **M**, the most
  involved piece but self-contained, no new deps, highly testable.
- Baseline LRU cache — **S**.
- `tools_browser.go` registration + handler wiring — **S**, follows the
  existing `handleScreenshot`/`handleTurboSnapshot` patterns closely.
- content.js `MutationObserver` + 3 dispatch actions + overlay-freeze — **M**,
  content.js is large and the overlay-filtering needs care.
- daemon pass-through routing — **S**.
- Native helper `/diff` endpoint — **deferred / optional**, not in the first
  cut.

A lean first cut (Go pixel diff + baseline cache + DOM signal, no thumbnail, no
native `/diff`) is comfortably **S–M** and delivers most of the value.

**Risks:**

- *Noise tuning.* The biggest risk is false positives from JPEG/AA/reflow noise
  making `changed` untrustworthy. Mitigated by conservative defaults
  (`min_score 0.997`), the DOM signal as a cross-check, and thorough
  `diff_test.go` coverage. Defaults will need real-page tuning.
- *Overlay contamination.* TurboWeb's own cursor/toast/flash animations are
  drawn into the page and *will* diff as change if not frozen/masked. This must
  be solved (overlay-freeze + subtree filtering) or the tool is noisy by
  construction — treat it as a correctness requirement, not polish.
- *Capture cost not eliminated, only the second-image transfer.* The verify
  call still pays one `captureVisibleTab` (~0.5 s, focus-stealing). The DOM-only
  fast path (`pixel_skipped`) is what removes even that for the common no-op
  case — worth shipping early.
- *Scroll/animation aliasing.* Global scroll and steady animation are the two
  cases most likely to confuse agents; both are explicitly detected and flagged
  rather than silently mis-reported.
- *Service-worker eviction.* The `MutationObserver` lives in the content script
  (per-page, survives worker eviction), and the baseline cache lives in the Go
  process — neither depends on the background service worker staying alive, so
  eviction is not a correctness risk.
