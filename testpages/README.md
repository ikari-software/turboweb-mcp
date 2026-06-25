# Cross-origin frame test harness

Real pages for validating the `all_frames` frameId handshake (turboweb-d2w) and
the trusted-input value-replacement fixes (turboweb-32r) against a live browser.

## Why three ports

An origin is scheme + host + **port**. Serving the same pages on `:8080`, `:8081`,
`:8082` gives three distinct origins (A, B, C). A page on A embedding an iframe
from B is genuinely cross-origin — so the top frame *cannot* script into it, and
the only way in is the per-frame content script + the selector→frameId handshake.

## Run

```bash
node testpages/serve.js          # serves on 8080, 8081, 8082
# or: make testpages
```

Then, in the browser the extension is attached to, open the **top** page (origin A):

```
http://127.0.0.1:8080/
```

`launch_browser` to that URL, or `navigate` an attached tab there.

## Frame map

```
TOP (A, :8080)  http://127.0.0.1:8080/top.html
├─ #same      → frame.html on A (:8080)   same-origin
├─ #cross     → frame.html on B (:8081)   cross-origin
└─ #nested    → nested.html on B (:8081)
   └─ #grandchild → frame.html on C (:8082)   cross-origin, 2 hops (A→B→C)
```

framePaths to pass as the `frame` argument:

| Frame             | `frame`                  |
|-------------------|--------------------------|
| Same-origin (A)   | `#same`                  |
| Cross-origin (B)  | `#cross`                 |
| Grandchild (C)    | `#nested > #grandchild`  |

**Address frames by selector, never coordinates** — a framePath is a chain of
CSS selectors, and coordinates don't translate across an origin boundary.

## What each page instruments

Every `frame.html` shows a colored banner with its **label + origin**, a findable
phrase, several inputs, a click button with a counter, and a live **event log**.
The log + counters are how you confirm an action landed in the *right* frame.

- `#plain` — ordinary input.
- `#controlled` — a faithful "framework owns the value" input: its state only
  updates on `input` events, and a render loop reverts the field every ~0.4s if
  you set `.value` without firing `input`. So:
  - `fill_input` / `type_text` / `cdp_type` → **sticks** (we fire a real InputEvent).
  - naive `execute_js` `el.value='x'` → **reverts** (proves why the naive path fails).
  - watch `last InputEvent → type/data` to confirm InputEvent fidelity.
- `#prefilled` — starts with `REPLACE ME`. After a replace it must read *exactly*
  the new value (no leftover text).
- `#btn` — increments a click counter and logs focus.

## Suggested checks

Cross-origin reach (the headline feature):

```
find_text   query="findme-cross-b"        frame="#cross"
extract_text                              frame="#nested > #grandchild"
query_elements selector="#btn"            frame="#cross"
click       selector="#btn"               frame="#cross"          # btn-count → 1
fill_input  selector="#plain" value="hi"  frame="#nested > #grandchild"
```

Value replacement / trusted input (also works inside frames):

```
fill_input  selector="#prefilled" value="NEW"        frame="#cross"   # value === "NEW"
cdp_type    selector="#prefilled" text="NEW" clear=true frame="#cross"
cdp_click   selector="#prefilled" clickCount=3        frame="#cross"   # selects all
cdp_key     key="a" modifiers=["Meta"]                                 # select-all (macOS)
cdp_key     key="Backspace"                                            # deletes
fill_input  selector="#controlled" value="stuck"     frame="#cross"   # does NOT revert
```

Negative control (should visibly revert, proving the naive bug):

```
execute_js  code="document.querySelector('#controlled').value='ghost'"  # reverts in ~0.4s
```

## Notes

- All ports are loopback only; the server has no dependencies.
- Override ports with `PORTS=9000,9001,9002 node testpages/serve.js` — the pages
  derive B/C from consecutive ports, so the top page must be the lowest port.
- These pages are a test fixture; they are not part of the shipped extension.
