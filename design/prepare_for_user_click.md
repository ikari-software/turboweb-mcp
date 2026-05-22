# Design: `prepare_for_user_click`

Status: Proposed
Author: design doc
Layers touched: Go MCP tool, daemon (ws.go), background.js, content.js overlay

---

## 1. Problem & motivation — the honest handoff

TurboWeb's whole premise is that a human is *watching* the browser tab while an
agent drives it. The overlay (cursor, badge, intent toasts) exists precisely so
the human can follow along. But "the human is watching" is currently a
spectator role — every tool either succeeds or errors, and the agent is
expected to finish the job itself.

Some flows genuinely cannot be finished by automation, and pretending otherwise
produces the worst outcomes:

- **Auth-gated actions** — an OAuth consent screen, a 2FA prompt, a "re-enter
  your password to continue" interstitial. The agent should not have the
  credentials, and a synthetic click on "Allow" is a security smell.
- **Final-confirmation buttons** — "Delete account", "Transfer $4,200",
  "Submit order". Even when the agent *could* click, a human should make the
  irreversible decision. This is a policy choice, not a capability gap.
- **OS-level surfaces** — the native file picker, a print dialog, a Chrome
  permission bubble. These are outside the page DOM and outside `<all_urls>`;
  no content-script or BiDi input event can reach them.
- **`isTrusted`-guarded controls that even `cdp_*` can't activate** — some
  payment widgets, captchas, and DRM-gated players reject programmatic input
  entirely, including BiDi-synthesised "trusted" events.

Today an agent hitting one of these has only bad options: fake it with
`execute_js` (bypasses the overlay, dishonest, often blocked), retry `click` in
a loop (the human watches it fail repeatedly), or return a vague error and
stop. None of these respect the human who is *right there, watching*.

`prepare_for_user_click` makes the handoff a **first-class, honest tool**. The
agent does everything it legitimately can — find the control, scroll it into
view, highlight it, screenshot it — and then explicitly says: *"this last
action is yours."* It converts a failure mode into a designed interaction. The
philosophy: **automation that knows its limits and asks clearly is more
trustworthy than automation that fakes competence.**

This is not a fallback for "the agent couldn't find the right selector." It is
a deliberate verb the agent reaches for when the *correct* outcome is human
action. The tool description must make that distinction explicit so models
don't use it to paper over their own navigation mistakes.

---

## 2. Proposed MCP tool API

### Tool name

`prepare_for_user_click`

Verb-first, reads as what it does: *prepares* the page so the user can click.
It does not itself click. (Considered and rejected: `request_user_click`,
`handoff_to_user`, `defer_to_user` — the chosen name keeps the "the agent set
this up" framing rather than implying the tool blocks on the human.)

### Parameters

| Param | Type | Required | Description |
|---|---|---|---|
| `selector` | string | one of selector/x,y | CSS selector of the control the human should click. Resolved to a bounding box on the page side. Preferred. |
| `x`, `y` | number | one of selector/x,y | Viewport coordinates, for canvas / non-DOM targets where a selector doesn't apply. Lower fidelity — no scroll-into-view, no element-anchored highlight. |
| `hint` | string | **required** | Human-readable instruction shown to the watching person: *what* to click and *why* the agent is handing off. e.g. "Click **Allow** to grant calendar access — I can't approve OAuth scopes on your behalf." |
| `reason` | string enum | optional | Machine-readable category: `auth` \| `confirmation` \| `os_dialog` \| `untrusted_input` \| `other`. Drives copy and iconography; defaults to `other`. |
| `label` | string | optional | Short name for the control ("the Allow button"), used in the overlay banner when a full `hint` is too long for the banner line. |
| `tabId` | number | optional | Tab ID; omit for active tab. Consistent with every other tool. |
| `intent` | string | required (server-injected convention) | As with all TurboWeb tools — the live toast. For this tool the intent should narrate the handoff itself: "Handing off to you — the final confirmation is yours to click." |

`hint` is required because the entire point of the tool is the instruction. A
`prepare_for_user_click` with no hint is a silent highlight, which defeats the
honesty goal.

### Return shape

The tool returns **immediately** after the page is prepared and the screenshot
is captured. It does **not** block waiting for the human (see §4 for the
rationale). The MCP result is multi-content: an image plus a structured text
block.

```jsonc
// MCP CallToolResult content[]:
[
  { "type": "image", "mimeType": "image/jpeg", "data": "<base64>" },
  { "type": "text", "text": "<JSON below>" }
]
```

```jsonc
{
  "handoff": true,
  "status": "awaiting_user",        // always this on success
  "target": {
    "found": true,
    "selector": "button#allow",
    "label": "Allow",
    "bbox": { "x": 612, "y": 318, "width": 96, "height": 40 },
    "inViewport": true,             // false if it could not be scrolled fully into view
    "occluded": false               // true if another element covers the center point
  },
  "instruction": "Click the \"Allow\" button to grant calendar access. I can't approve OAuth scopes on your behalf — this one is yours.",
  "reason": "auth",
  "overlayShown": true,             // the on-page banner + highlight rendered
  "screenshotTaken": true
}
```

`instruction` is the agent-facing canonical phrasing — the agent should relay
it verbatim to its own user in chat. `handoff: true` is a stable flag the agent
(or an orchestrator) can branch on without parsing prose.

On failure to locate the target, see §7 — the tool returns
`status: "target_not_found"` with `handoff: false` and **no** image, so the
agent knows the handoff did not actually happen and can re-resolve the target.

---

## 3. Visual treatment — reusing the existing overlay

The overlay machinery in `content.js` (the `overlay` IIFE around line 746) is
almost entirely reusable. `prepare_for_user_click` introduces **one new overlay
kind** and otherwise composes existing primitives. Nothing here requires new
animation code beyond the banner.

### Reused as-is

- **`moveCursorTo(x, y)`** — animate the agent cursor to the target's center,
  exactly as `click` does in `showStart`. This gives the human the familiar
  "the agent is pointing here" motion before the handoff.
- **`highlightRect(rect, palette)`** — the dashed/glowing box (`.highlight`
  class, `highlightFade` keyframe at content.js ~963). For the handoff we want
  it to **persist**, not fade after 1200 ms. Add a `persist` option:
  `highlightRect(rect, palette, { persist: true })` skips the
  `setTimeout(... remove, 1200)` and instead keeps a reference so a later
  `clear` event can remove it. The CSS gets a `.highlight.persist` variant that
  drops `animation: highlightFade ...forwards` and uses a slow 2 s
  breathing pulse (`box-shadow` oscillation) so the box stays alive and
  eye-catching without being a static border the eye filters out.
- **`setAnchor(el, x, y)`** — glue the persistent highlight + cursor to the
  element so they track if the page scrolls or reflows while the human reads
  the banner. This is the same anchoring `click`/`type_text` already use.
- **`flashAt(x, y, palette)`** — a single ripple on arrival, to land the
  "here" beat. Reused unchanged.
- **`cameraFlash()`** — fired when the screenshot is captured, same as
  `screenshot`/`turbo_snapshot`. Free, already wired.
- **`showToast(intent, who)` / `setBadge(...)`** — the intent toast and badge
  fire through the normal `showStart` path. The intent for this tool narrates
  the handoff, so no special-casing is needed for the toast itself.
- **`actionPalette(action)`** — hue-derived palette. `prepare_for_user_click`
  gets its own palette entry (see §6) so the highlight and banner read as
  "handoff" rather than "click".

### New: the handoff banner

The one genuinely new element is a **handoff banner** — a prominent on-page
callout that carries the `hint`. Toasts are deliberately low-key, auto-fade,
and go transparent on mouse-proximity (they're *narration*). A handoff is an
*instruction the human must act on* — it should be sticky and unmissable.

The banner is a new node inside the existing shadow-DOM `root` (so it inherits
the `--client-hue` custom property and the `z-index: 2147483647` host). Design:

- A pill/card anchored top-center (below where toasts stack, or replacing that
  zone while active), with the agent's hue as accent border and the
  `agent-mark` robot glyph reused from the badge.
- Content: bold first line = `label` or a short slug, body = `hint`, plus a
  small caption "TurboWeb · the agent is waiting for you".
- A thin **connector line / arrow** from the banner to the highlighted target
  rect, drawn as an SVG inside `root`. This is the only net-new drawing
  primitive; it can be a single `<svg><line>` repositioned on the same
  rAF/anchor tick that already moves the cursor. If the connector is judged
  too costly for v1, ship without it — the persistent highlight + cursor
  resting on the target already establish the spatial link.
- Unlike toasts, the banner does **not** fade on mouse-proximity and does
  **not** auto-expire on the 15 s/60 s toast timers. It clears only on an
  explicit `clear` event (§4) or after a long safety ceiling (§7).

The banner reuses the toast's enter transition pattern (offset → `requestAnimationFrame` → `.on` class) and the `escapeHtml` helper for `hint`/`label` — `hint` is agent-supplied text and **must** be escaped, same as toast text already is.

---

## 4. Notifying the human, and how the agent learns they acted

### Three notification surfaces, by reach

1. **On-page banner + persistent highlight + cursor** (primary). The human is
   watching the tab; the banner is in their eyeline, anchored to the control.
   This is the surface that does the real work.
2. **Extension popup** (secondary). The popup already receives a `broadcast`
   activity feed from `background.js` (`popupPorts`, `logActivity`). A
   `prepare_for_user_click` call gets a distinct, sticky popup row — "Waiting
   for you: click Allow" — styled in the agent's hue, pinned to the top of the
   activity list and not scrolled away by subsequent telemetry. This covers the
   case where the human is looking at the popup, not the page.
3. **System notification** (opt-in, escalation). `chrome.notifications` would
   surface a desktop toast even when the browser is backgrounded. This requires
   adding `"notifications"` to `manifest.json` `permissions` (currently
   `["tabs","activeTab","scripting","debugger","alarms"]`). Recommendation:
   **defer to v2**, gated behind a popup setting, because (a) it's a new
   permission prompt that affects the store listing, and (b) for the core "human
   is watching" use case the on-page banner is sufficient. Note it in the design
   so it isn't re-litigated.

### How the agent learns the human acted: instruct-and-stop, with opportunistic detection

The tool **does not block**. It returns `status: "awaiting_user"` immediately.
Rationale:

- MCP tool calls should not hang for an unbounded human-scale duration. A tool
  that blocks for two minutes waiting on a person breaks client timeouts and
  ties up the daemon's request slot.
- "Instruct and stop" is the *honest* shape: the agent's turn ends, it tells
  its user "I've highlighted the Allow button — click it and tell me when
  you're done," and control returns to the human in the chat as well as in the
  browser. The handoff is symmetric across both surfaces.

The agent resumes when its user tells it to (the natural conversational loop),
and then **verifies** by observing page state — `find_text`, `screenshot`, or
checking that the gated content is now reachable. The agent should not assume
success; it should confirm.

**Opportunistic detection (nice-to-have, not the contract):** because the
banner anchors to a real element, content.js *can* attach a one-shot
`click`/`pointerdown` listener on the target element while the banner is
active. When the human clicks it, content.js:

- clears the banner + persistent highlight locally (instant feedback — the box
  turns into a brief green success pulse, reusing `flashAt` with a success
  palette), and
- emits a fire-and-forget telemetry message up through `background.js`
  (`broadcast`) so the popup row flips to "Done — user clicked Allow".

This is **observation, not synchronisation**: it improves the popup/overlay UX
but the agent never depends on it. A separate lightweight tool —
`check_user_click(tab)` — can expose that observed state if an orchestrator
genuinely needs to poll (returns `{ acted: bool, since_ms }`). Polling is the
exception; instruct-and-stop is the default and the documented path.

If the human clicks something *else* or never clicks, the banner stays up until
cleared/timed-out (§7).

---

## 5. Implementation across layers

### Go MCP tool (`tools_interaction.go`)

New tool registered in `registerInteractionTools`. Unlike the pure
`passThrough` tools, this one needs a custom handler because it (a) resolves the
selector to a bbox, (b) drives a scroll-into-view, (c) captures a screenshot,
and (d) assembles a multi-content (image + text) result.

The handler composes existing daemon plumbing rather than inventing transport:

1. `resolveContext(args["tabId"])` — same as the BiDi handlers.
2. Resolve the target. With a selector, reuse `resolveSelectorCenter` /
   `resolveSelectorRect` (the helper `cdp_click` already uses). With `x,y`,
   skip resolution.
3. Send a new extension action `prepare_for_user_click` (via `send(...)`,
   `rawArgs(args)`) carrying `selector`/`x,y`, `hint`, `label`, `reason`. The
   extension does the scroll-into-view, the overlay banner, and reports back the
   resolved bbox + `inViewport`/`occluded` flags.
4. Capture the screenshot by reusing the existing `screenshot` path
   (`handleScreenshot` logic / `bidiScreenshot` with extension fallback) so the
   returned image is consistent with the `screenshot` tool. The screenshot is
   taken *after* the overlay renders, so the highlight + banner are visible *in
   the returned image* — the agent sees what the human sees.
5. Build the result with `mcp.NewImageContent(...)` + `mcp.NewTextContent(...)`
   carrying the JSON from §2. (`tools_browser.go`'s `handleScreenshot` already
   shows the `NewImageContent` pattern.)

The tool description must steer usage: explicitly say *use this when human
action is genuinely required (auth, irreversible confirmation, OS dialogs,
controls that reject synthetic input) — not as a fallback for selectors you
couldn't resolve.*

### Daemon (`ws.go`)

Minimal change. `ws.go` already injects `_intent`, `_clientLabel`,
`_clientType`, `_clientHue` into every command's params (lines ~225–233, 699–711)
and routes by `id`. `prepare_for_user_click` rides the existing request/response
path; no new message type is needed for the main call.

One addition: the **opportunistic detection** telemetry (§4) and any
`check_user_click` polling are server-initiated *push* shaped — they look like
the existing `mcp_clients` / `stats` broadcasts. If `check_user_click` is built,
the daemon needs to relay the content-script's "user clicked" event; the
cleanest path is the extension holding that state per-tab and answering a
`check_user_click` request synchronously, so `ws.go` needs no new push channel —
it stays pure request/response.

### background.js

- Add `prepare_for_user_click` to the `dispatch` switch (~line 822). It is a
  content-script action (it touches the DOM: scroll, overlay), so it routes via
  `toContent(tabId, 'prepare_for_user_click', params)` like `inspect` /
  `query_elements`.
- Add `prepare_for_user_click` to `PAGE_ACTIONS_THAT_GATE_ON_CURSOR` (~line 415)
  so the daemon-side flow waits for the cursor + scroll + banner to finish
  rendering before the Go handler proceeds to take the screenshot. The current
  900 ms race ceiling may need a modest bump (e.g. 1500 ms) because
  scroll-into-view + smooth-scroll settle takes longer than a bare cursor hop;
  the content script should also resolve `sendResponse` only once the scroll has
  settled and the banner painted.
- For the popup: emit a distinct `logActivity` entry / `broadcast` so the popup
  can render the sticky "waiting for you" row. Optionally a new broadcast
  `type: 'handoff'` carrying `{ active, label, hint, hue, tabId }` so the popup
  has a dedicated, pinned UI region rather than a regular activity row.

### content.js (overlay)

This is where most of the work lands, but it is composition:

- **`resolveTarget`** (~line 1568): add a `prepare_for_user_click` branch that
  resolves `selector` (or `x,y`) to `{ el, x, y, bbox }`, mirroring the `click`
  branch.
- **New `dispatch` handler** for `prepare_for_user_click` that:
  1. resolves the element,
  2. calls `el.scrollIntoView({ block: 'center', behavior: 'smooth' })` and
     waits for scroll to settle (a short `scrollend`/timeout race),
  3. re-reads `getBoundingClientRect` post-scroll,
  4. computes `inViewport` (rect fully within `innerWidth/innerHeight`) and
     `occluded` (`document.elementFromPoint(centerX, centerY)` is the target or
     a descendant),
  5. returns `{ found, bbox, inViewport, occluded }` to background.js so the Go
     layer can put it in the result JSON.
- **New overlay event kind** in the `__turbo_overlay` router (~line 1802):
  alongside `start` / `result` / `error`, add `handoff` and `handoff_clear`.
  Cleaner than overloading `start`, because the handoff has a persistent
  lifecycle the existing kinds don't.
  - `handoff`: `moveCursorTo` the target, `flashAt`, `highlightRect(rect, palette, { persist: true })`, `setAnchor`, render the banner with `hint`/`label`, fire `cameraFlash` for the screenshot beat.
  - `handoff_clear`: remove the persistent highlight + banner, optional success
    pulse, `clearAnchor`.
- **Banner functions**: `showHandoffBanner({ hint, label, hue })` and
  `clearHandoffBanner()`, living next to `showToast`/`exitToast`, reusing
  `ensure()`, `escapeHtml`, the shadow `root`, and the enter-transition pattern.
- **`actionPalette`**: add a `prepare_for_user_click` entry — a calm,
  attention-holding palette distinct from the orange click flash and the red
  error (see §6).
- **Opportunistic listener** (optional, §4): while a banner is active, a
  one-shot listener on `target.el` that on click calls `clearHandoffBanner()`
  with a success pulse and posts telemetry up.

---

## 6. Interaction with multi-agent sessions

Multi-agent is already a solved problem in this codebase and
`prepare_for_user_click` inherits the solution:

- **Hues already exist.** `computeClientHues` in `ws.go` assigns each relay
  client a stable hue (brand 40°, then +45° steps). `_clientHue` rides into the
  extension and `showStart` applies it to `--client-hue`, tinting cursor, badge,
  and toast. The handoff banner and persistent highlight read `--client-hue`
  the same way — so a handoff from Agent B is visibly *Agent B's* handoff
  (its hue on the banner border, robot mark, and highlight glow). The human
  immediately knows *which* agent is asking.

- **The banner shows the agent label.** `clientLabel` is already plumbed
  through (`info.label`, `_clientLabel`, `showStart`'s `display`). The banner
  caption includes it: "Claude/research-agent is waiting for you" — essential
  when two agents drive two tabs.

- **One banner per tab.** The banner is a per-tab DOM element. Two agents
  acting on two different tabs each show their own banner in their own tab —
  no conflict. The realistic collision is two agents racing on the *same* tab,
  which is already chaotic for every other tool. Rule: a new `handoff` on a tab
  that already has an active banner **replaces** it (newest handoff wins), and
  the popup's pinned handoff region shows the most recent. The replaced agent's
  highlight is cleared. This matches the existing "newest action bumps the
  toast" model and avoids stacking competing instructions on one control.

- **Popup.** The popup already renders per-client info with hues. The pinned
  "waiting for you" region is keyed by tab; if multiple tabs have pending
  handoffs, the popup lists them, each in its agent's hue.

No new multi-agent machinery is needed — the feature is hue-aware by
construction because it reuses the hue-aware primitives.

---

## 7. Edge cases

| Case | Behavior |
|---|---|
| **Selector matches nothing / zero-size bbox** | Return `status: "target_not_found"`, `handoff: false`, `target.found: false`, **no image**, no banner. The agent must re-resolve and retry — this is the one case where the tool "fails" rather than handing off. Mirrors `cdp_click`'s zero-size-bbox error. |
| **Selector matches multiple elements** | Use the first, set `target.ambiguous: true` in the result so the agent can warn its user. Don't error — first-match is the established convention (`document.querySelector`). |
| **Target offscreen** | `scrollIntoView({ block: 'center' })` handles the normal case. If still not fully visible after scroll (fixed-position overflow, oversized element), set `inViewport: false`; banner still renders, highlight clamps to the visible portion. Agent relays "scroll down to find it" via the hint. |
| **Target inside an iframe** | The overlay deliberately renders **only in the top frame** (content.js ~860). A selector inside a same-origin iframe can't be resolved by a top-frame `querySelector`, and the highlight can't be drawn over cross-frame content reliably. v1: return `target.found: false` with `reason_detail: "target_in_iframe"` and instruct the agent to fall back to a coordinate (`x,y`) handoff — coordinate mode still works because the banner + highlight are viewport-positioned. Full in-iframe support (frame-aware overlay) is out of scope; note it as future work. |
| **Target occluded by another element** | `elementFromPoint` check sets `occluded: true`. Banner still shows; hint should mention it ("a cookie banner is covering it — dismiss that first"). The agent can choose to handle the occluder itself before re-handing-off. |
| **Human never acts** | The banner is persistent by design but not eternal. A safety ceiling (e.g. 10 minutes) auto-clears the banner + highlight so a forgotten handoff doesn't leave a permanently overlaid page; the popup row flips to "handoff expired". This ceiling is generous — much longer than the toast's 60 s — because human response time is the expected variable. The agent, having already returned, is unaffected. |
| **Human navigates away / reloads the tab** | Content script is re-injected fresh on navigation; the banner is gone with the old document. Acceptable — the handoff context is stale anyway. The popup's pinned row should clear on `tabs.onUpdated`/navigation for that tab. |
| **`chrome://` or other restricted page** | `notifyOverlay` already no-ops where the content script can't be injected. The tool returns `overlayShown: false`; the screenshot may still succeed via `captureVisibleTab`/BiDi, and the `instruction` text still carries the handoff. Honest degradation. |
| **Tab backgrounded during prepare** | The cursor animation throttles on backgrounded tabs (the existing rAF concern). The 900→1500 ms gate race ceiling already covers this; worst case the screenshot is taken before the banner fully paints — acceptable, the `instruction` text is the real payload. |
| **`x,y` coordinate mode** | No element to scroll, anchor, or attach a click listener to. Skip scroll-into-view; draw the highlight as a fixed box around the point; `inViewport` is whether the point is on-screen. Opportunistic detection is unavailable in this mode — `instruct-and-stop` only. Documented as the lower-fidelity path. |

---

## 8. Testing approach

The codebase has Go tests (`*_test.go`, including `ws_test.go`,
`tools_handler_test.go`) and a Vitest suite for the extension
(`extension/__tests__/{content,background,popup}.test.js`).

**Go (`tools_interaction` handler):**
- Table-driven test for the handler: selector-resolves, selector-missing
  (`target_not_found`), `x,y` mode, missing `hint` (validation error).
- Assert the result is multi-content (image + text) on success and text-only on
  `target_not_found`.
- Reuse the existing fake-extension / mock `send` harness in
  `tools_handler_test.go` to assert the `prepare_for_user_click` action and its
  params (`hint`, `label`, `reason`) are forwarded intact.

**Extension Vitest:**
- `background.test.js`: `dispatch('prepare_for_user_click', ...)` routes to
  `toContent`; the handoff `broadcast`/`logActivity` entry is emitted; the
  action is in `PAGE_ACTIONS_THAT_GATE_ON_CURSOR`.
- `content.test.js`: `resolveTarget('prepare_for_user_click', ...)` resolves a
  selector; `scrollIntoView` is invoked; `inViewport`/`occluded` flags computed
  correctly (jsdom with stubbed `getBoundingClientRect`/`elementFromPoint`);
  the `handoff` overlay event creates a persistent highlight (no fade timer)
  and a banner node with escaped `hint`; `handoff_clear` removes both; a
  second `handoff` on the same tab replaces the first; `escapeHtml` is applied
  to a `hint` containing `<script>`.

**Manual / integration (the part that matters most):**
A scripted manual checklist on a real browser, since the feature *is* a visual
UX: (1) handoff to a visible button — banner + highlight + cursor land, button
appears in the screenshot; (2) handoff to a below-fold button — scrolls into
view first; (3) two agents, two tabs — each banner in its own hue; (4) OAuth
consent screen — realistic `auth` scenario end-to-end; (5) human clicks the
target — opportunistic success pulse fires, popup flips to "done"; (6) human
ignores it — banner persists, then expires after the ceiling. Capture
before/after screenshots for the PR (the repo has a demo-reel habit).

---

## 9. Effort estimate & risks

**Estimate: M** (roughly 2–3 focused days).

Breakdown:
- Go tool + handler + multi-content result: **S** — composes existing
  `resolveSelector*`, `screenshot`, `NewImageContent` plumbing.
- background.js dispatch + gate-set + popup broadcast: **S**.
- content.js: `resolveTarget` branch, scroll-into-view + flag computation,
  `handoff`/`handoff_clear` overlay events, persistent-highlight option: **S–M**.
- The handoff banner (new UI, the connector arrow, CSS, escaping, lifecycle):
  **M** — the only genuinely new visual component; everything else is reuse.
- Tests: **S–M**.

The optional pieces (`chrome.notifications`, `check_user_click` polling, the
SVG connector line, opportunistic click detection) are each independently
deferrable and would push toward **L** if all included in v1. Recommend
shipping the core (banner + highlight + screenshot + instruct-and-stop) first.

**Risks:**

- **Misuse by models** — the biggest risk. Agents may reach for
  `prepare_for_user_click` whenever a `click` is hard, turning an honesty tool
  into a "give up" tool and pestering the human. Mitigation: a sharply worded
  tool description that enumerates legitimate triggers (auth, irreversible
  confirmation, OS dialogs, untrusted-input rejection) and explicitly says it is
  *not* a fallback for unresolved selectors. The `reason` enum nudges the model
  to justify the handoff. Worth monitoring in real usage.
- **Persistent overlay annoyance** — a sticky banner that doesn't fade is more
  intrusive than a toast. Mitigation: the 10 min ceiling, the proximity-fade
  could be applied to the banner *body* but not the highlight, and the
  newest-wins replacement rule prevents accumulation.
- **Iframe gap** — a real functional limitation in v1. Documented; coordinate
  mode is the escape hatch. Acceptable for a first release given the overlay's
  existing top-frame-only constraint.
- **Screenshot timing** — the screenshot must capture the banner+highlight, so
  the prepare step must fully settle before capture. The gate ceiling makes
  this best-effort; on a throttled background tab the image may miss the
  overlay. Low severity — the `instruction` text is the contract, the image is
  the aid.
- **Honesty regression if the agent fakes the follow-up** — the tool is honest
  only if the agent actually stops and waits rather than immediately calling
  `cdp_click` on the same target. This is a prompt/behavior concern, not a code
  one; the `agent-rules` prompt should state that after `prepare_for_user_click`
  the agent ends its turn and waits for the human.
