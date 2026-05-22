# Design: `try_url_prefill` — URL-parameter form prefilling

Status: Proposed
Author: design doc
Scope: new MCP meta-tool + one new extension content-script handler

## 1. Problem & motivation

A large class of web apps prefill form fields from URL query parameters. The
canonical example is Temporal Web UI's
`/start-workflow?workflowId=X&taskQueue=Y&workflowType=Z` — opening that URL
lands the user on the start-workflow form with three required fields already
populated. The same pattern shows up in Jira issue creators, GitHub's
`/issues/new?title=&body=&labels=`, Grafana dashboard links, search/filter
pages, and countless internal tools.

Today an agent fills such a form the slow way: `turbo_snapshot` to see the
fields, then a `click` + `type_text` round per field (often `cdp_*` variants
when the field is React-controlled), then a verifying read. Six-plus tool
calls, six-plus animated overlay actions on a screen a human is watching, and
several hundred milliseconds of round-trip each. One `navigate` to a
well-formed URL collapses all of that into a single page load — and the human
sees one clean navigation instead of a flurry of synthetic typing.

The catch: the agent has to *know* the field→param mapping, and it has to
*verify* the prefill actually worked, because URL prefilling is a per-app
convention with no standard. `try_url_prefill` is a meta-tool that does the
discovery, the URL construction, the navigation, and the verification in one
call, and tells the agent exactly which fields it still has to fill by hand.

It is deliberately a *try* tool: it never claims success it didn't verify, and
it degrades gracefully to "here are the N fields you still need to type into."

## 2. Proposed MCP tool API

Registered in a new `registerFormTools(s)` (file `tools_forms.go`), called
from `main.go` alongside the existing `registerDomTools` etc. Style matches
`tools_dom.go` — `mcp.NewTool` with typed params, a handler that calls
`send()` and returns `textResult`.

```
try_url_prefill
  Description: "Prefill a form by constructing a URL with query parameters,
    navigating to it, and verifying which fields were populated. Many web
    apps (Temporal, Jira, GitHub issue creators) prefill required fields from
    URL params — this collapses a multi-step click+type sequence into one
    navigation. Returns per-field success and which fields still need manual
    filling."

  Params:
    url       string  (required) Base URL of the page hosting the form,
                       WITHOUT the prefill params (existing params preserved).
    formData  object  (required) Logical field name -> desired string value,
                       e.g. {"workflowId":"order-42","taskQueue":"orders"}.
                       Keys are matched against the form's inputs (see §3).
    formSelector string (optional) CSS selector to disambiguate when the page
                       has multiple forms. Omit to use the first/largest form.
    tabId     number  (optional) Tab ID (omit for active tab).
    apply     boolean (optional, default true) If false, do NOT navigate —
                       return the planned URL and mapping only (dry run).
    settleMs  number  (optional, default 1200) How long to wait after
                       navigation before verifying, to let SPAs hydrate.
```

### Return shape

`textResult` JSON, modeled on `type_text`'s verify-or-explain style:

```json
{
  "prefilled": true,
  "url": "https://temporal.example/start-workflow?workflowId=order-42&taskQueue=orders",
  "navigated": true,
  "fields": [
    { "name": "workflowId",   "param": "workflowId", "selector": "#workflow-id-input",
      "status": "verified",   "actualValue": "order-42" },
    { "name": "taskQueue",    "param": "taskQueue",  "selector": "input[name=taskQueue]",
      "status": "verified",   "actualValue": "orders" },
    { "name": "workflowType", "param": null,         "selector": "#wf-type",
      "status": "unmapped",   "reason": "no URL param discovered for this field" }
  ],
  "manualFields": [
    { "name": "workflowType", "selector": "#wf-type", "value": "OrderWorkflow",
      "hint": "fill with type_text or cdp_type" }
  ],
  "summary": "2/3 fields prefilled via URL; 1 needs manual filling"
}
```

`status` per field is one of:
- `verified` — param was added to the URL and the field's value matches after load.
- `unmapped` — no URL param could be discovered for this logical field.
- `mismatch` — param was sent but the loaded field's value differs (app ignored
  it, transformed it, or the param name guess was wrong).
- `missing` — the field that the param was inferred from no longer exists after
  navigation (SPA route change, conditional render).

`manualFields` is the actionable list: every field whose value the agent still
needs to set, with a selector and the intended value, ready to feed straight
into `type_text` / `cdp_type`. When `apply:false`, `navigated` is false and
`fields` carries the *planned* mapping with `status:"planned"`.

Failure modes return `textResult` with `prefilled:false` and an `error`
string rather than an MCP error, so the agent always gets a structured verdict
(same philosophy as `connection_status`). An MCP error is reserved for
transport failures (`send()` itself failing).

## 3. How field→param mapping is discovered

Discovery runs in two phases. **Phase A** (pre-navigation) inspects the form
*as currently loaded* — the tool first needs the page at `url` open, or close
to it, to see the form. **Phase B** is the post-navigation verification.

For Phase A the tool navigates to `url` *without* params first (if not already
there), then runs a new `inspect_form` content-script handler that returns,
for every form on the page (or the one matching `formSelector`):

- the form's `action` and `method`,
- each field's `tag`, `type`, `name`, `id`, `placeholder`, associated
  `<label>` text, `aria-label`, `required`, `disabled`, current `value`,
- whether the field is `contenteditable` or a framework-custom widget.

The Go layer then maps each `formData` key to a URL param name using a
ranked heuristic cascade — first match wins, ties broken by rank:

1. **Form-action introspection (highest confidence).** If the form's `action`
   (or the page URL) already carries query params, those param *names* are the
   ground truth for what the server accepts. A field whose `name`/`id` equals
   such a param maps directly. This also catches the Temporal case where the
   start-workflow link template is discoverable.
2. **Exact name/id match.** `formData` key === field `name`, or === field
   `id`. The overwhelmingly common case (`workflowId` field has
   `name="workflowId"`). The param name used is the field's `name` (falling
   back to `id`), because servers read `name`.
3. **Normalized match.** Compare after lowercasing and stripping
   `-`/`_`/spaces: `taskQueue` ↔ `task_queue` ↔ `task-queue`. The emitted
   param name is still the field's real `name`.
4. **Label / placeholder / aria-label match.** Normalized fuzzy match of the
   `formData` key against the field's visible label text. Lower confidence;
   used only when 1–3 produce nothing.
5. **Known-pattern table.** A small static table of well-known apps keyed by
   URL host/path substring (e.g. host contains `temporal` + path
   `/start-workflow` → `{workflowId, taskQueue, workflowType}` are param-
   addressable). This is a tie-breaker / fallback, not the primary path —
   the heuristics above should cover most apps without it. The table lives
   in Go (`knownPrefillPatterns`) and is cheap to extend.

A field that matches none of these is emitted as `unmapped`. A `formData` key
that matches no field at all is reported in `manualFields` with
`selector:null` and a `reason` — the agent asked to fill something that isn't
on this form.

Confidence is tracked per field (`high` for rules 1–3, `low` for 4–5) and
surfaced so the agent can decide whether to trust a low-confidence prefill or
just fill manually. Only `high`-confidence and rule-1 mappings are written
into the URL by default; `low`-confidence fields are routed to `manualFields`
unless the caller is fine with speculative params (a future `aggressive`
flag — out of scope here).

The URL is built in Go with `net/url`: parse `url`, merge discovered params
into the existing query (existing params preserved, prefill params override),
re-encode. This correctly handles escaping and pre-existing query strings.

## 4. Implementation across layers

The tool is a Go-side orchestrator. It reuses existing actions where possible
and adds exactly one new extension capability (`inspect_form`).

### Go tool (`tools_forms.go`)

`handleTryURLPrefill` orchestrates:

1. Parse args (`getString`, `getBool`, `rawArgs` from `util.go`).
2. **Locate the form.** Call `send("inspect_form", {tabId, url, formSelector})`.
   The extension handles "navigate to base URL if not already there, then
   inspect" — see below — so the Go side gets the form descriptor in one
   round trip.
3. **Build the mapping** (§3) purely in Go. No browser involvement; this is
   string heuristics over the form descriptor.
4. **Construct the URL** with `net/url`.
5. If `apply:false`, return the plan now (`status:"planned"`).
6. **Navigate**: `send("navigate", {tabId, url: builtURL})`. Reuses the
   existing `navigate` action verbatim — overlay shows the purple navigate
   ripple, human sees one clean load.
7. **Wait + verify.** Sleep `settleMs` (Go-side `time.Sleep`, capped ~5s),
   then `send("inspect_form", {tabId, formSelector})` again and compare each
   mapped field's `value` to the intended value. Normalize whitespace; treat
   the field's own transformation (trim, case) leniently — exact match →
   `verified`, non-empty-but-different → `mismatch`, empty → `mismatch`,
   field gone → `missing`.
8. Assemble the return JSON, route unfilled fields to `manualFields`, return
   via `textResult`.

The whole tool is one `send`/`navigate`/`send` sequence — the Go layer holds
the state machine; the extension stays a set of stateless verbs. The MCP
`intent` param required by every tool is set by the agent as usual; the tool
itself adds no new overlay primitives.

### Daemon (`ws.go`)

**No changes.** `inspect_form` is just another action string; `sendDirect`'s
`tabId` routing, the relay path, and timeout handling all apply unchanged.
`navigate` already works. The only consideration: the verify `send()` should
pass an explicit timeout (e.g. `send("inspect_form", params, 8000)`) so it
doesn't inherit the 30s default while still tolerating a slow SPA.

### `background.js`

Add one `case 'inspect_form':` to the `dispatch()` switch (~line 935, next to
`get_page_structure`). It is a content-script command, so it forwards via
`toContent()`:

```
case 'inspect_form':
  return await toContent(params.tabId, 'inspect_form', {
    selector: params.formSelector,
  });
```

One subtlety: `inspect_form` may receive an optional `url`. If `url` is
present and the tab is not already on that origin+path, `background.js`
performs `chrome.tabs.update(tid, {url})` first (reusing the `navigate`
logic), waits for the tab `status === 'complete'` via a one-shot
`chrome.tabs.onUpdated` listener, *then* calls `toContent`. This keeps the
"navigate to base, then inspect" step a single action from Go's perspective.
If `url` is omitted (the verify call), it just inspects the current page.

### `content.js`

Add `inspect_form` to the `handlers` map (~line 1843) and implement
`inspectForm({selector})`:

- Resolve the target form: `document.querySelector(selector)` if given,
  else the form with the most fields (largest visible `<form>`); if there are
  no `<form>` elements, fall back to a container of inputs (many SPAs skip
  `<form>`).
- For each `input`/`select`/`textarea`/`[contenteditable]` descendant, emit
  `{tag, type, name, id, placeholder, label, ariaLabel, required, disabled,
  value, visible, frameworkControlled}`. Label text is resolved via
  `<label for=id>`, wrapping `<label>`, or `aria-labelledby` — reuse the
  label-resolution logic already present in `getInteractiveMap` /
  `queryElements`.
- `value` reading mirrors the verification logic in `typeText`: read
  `.value` for native inputs, `.textContent` for `contenteditable`. This is
  the same value the agent would see, so verification is consistent.
- Return `{form: {action, method, selector}, fields: [...]}`.

`inspectForm` is a pure read — no overlay animation needed (consistent with
other read handlers), though a subtle highlight of the form is a nice-to-have.

## 5. Edge cases & failure modes

- **Param doesn't map to anything** — `formData` key matches no field:
  reported in `manualFields` with `selector:null`; the agent knows it asked
  for a nonexistent field.
- **Field has no URL param** — common; field exists but the app doesn't read
  it from the URL. Emitted `unmapped`, routed to `manualFields`. This is the
  expected "2/3 prefilled" partial-success case.
- **App ignores / sanitizes the param** — URL carried `workflowId=order 42`
  but the field loads empty or trimmed. Verification catches it as
  `mismatch`; field goes to `manualFields`. No false success.
- **SPA reads params asynchronously** — the field is empty immediately after
  load but populates 800ms later when the router/store hydrates. Mitigated by
  `settleMs` (default 1200ms). Optionally the verify step can poll: re-inspect
  every 300ms up to `settleMs` and stop early once all mapped fields are
  non-empty — cheaper on fast pages, robust on slow ones.
- **Field is framework-controlled and rejects URL prefill but accepts only
  typed input** — rare, but verification reports `mismatch`; the field falls
  back to `manualFields` and the agent uses `cdp_type`.
- **Param triggers a server redirect** (e.g. `?workflowId=` redirects to a
  login or a 404) — after navigation the form is gone; all fields verify as
  `missing`, `prefilled:false`, `error` explains the URL changed. Compare
  `location` post-navigation against the intended URL's path.
- **Value would make a malformed/oversized URL** — extremely long values
  (multi-KB body text) blow past URL length limits. The Go layer caps total
  URL length (~2000 chars conservative); fields whose inclusion would exceed
  it are demoted to `manualFields` with `reason:"value too long for URL"`.
- **Multiple forms on the page** — `formSelector` disambiguates; without it
  the largest form is chosen and its selector is returned so the agent can
  see which one was used.
- **CSP / cross-origin** — `navigate` and `inspect_form` run through the
  extension, which is not subject to page CSP; no issue. An `about:` /
  `chrome://` target is rejected up front.
- **Existing query params on `url`** — preserved; prefill params merged in,
  colliding keys overridden by `formData`.
- **`navigate` succeeds but tab never reaches `complete`** — the
  `onUpdated` wait in `background.js` has its own timeout; on expiry it
  proceeds to inspect anyway and verification reports whatever it finds.

## 6. Testing approach

### Go tests (`tools_forms_test.go`)

Pure functions are the bulk of the logic and are unit-testable without a
browser, following `util_test.go` / `tools_handler_test.go` patterns:

- **Mapping heuristics** — table-driven: given a synthetic form descriptor
  and a `formData` map, assert the produced field→param mapping, param names,
  and confidence levels. Cover each rule (exact, normalized, label, known-
  pattern) and the unmapped case.
- **URL construction** — given base URL (with and without pre-existing query)
  + param map, assert the exact encoded URL; cover escaping, collisions,
  length-cap demotion.
- **Verification logic** — given intended values + a post-nav form
  descriptor, assert each field's `status` (verified / mismatch / missing)
  and the `manualFields` partition.
- **Handler integration** — the existing test harness can stub `send()` to
  return canned `inspect_form` / `navigate` responses (see how
  `tools_handler_test.go` exercises handlers) and assert the final
  `textResult` JSON for the dry-run and full-apply paths.

### Vitest extension tests (`extension/__tests__/content.test.js`)

`inspect_form` is a DOM function — test it with the existing jsdom-based
content.test.js harness:

- Build a jsdom `<form>` with named inputs, a `<select>`, a `contenteditable`
  div, `<label for>` associations, a `required` field, a `disabled` field.
  Assert `inspectForm` returns the right descriptor: names, labels resolved,
  values, flags.
- Multi-form page → assert largest-form selection and that `selector`
  disambiguation works.
- No-`<form>` page (bare inputs) → assert the container fallback.
- Value reading parity with `typeText`'s verify path (native `.value` vs
  `contenteditable` `.textContent`).

`background.test.js`: add a case for the new `dispatch` branch — assert
`inspect_form` with a `url` triggers `chrome.tabs.update` then `toContent`,
and without `url` calls `toContent` directly. The existing chrome mock
(`chrome.tabs.update`, `chrome.tabs.onUpdated`) covers this.

## 7. Effort estimate & risks

**Estimate: M** (roughly 1–2 focused days).

Breakdown:
- `inspect_form` content handler + background dispatch case — **S**. New code
  but it closely mirrors `queryElements` / `getInteractiveMap`; label
  resolution and value reading already exist to copy.
- Go orchestrator + mapping heuristics + URL builder — **M**. The heuristic
  cascade and verification state machine are the real work, but they're pure
  Go and well-bounded.
- Tests across both layers — **S–M**, mostly table-driven.

What keeps it out of **S**: the mapping heuristics are judgement-heavy and
the verification needs the navigate→settle→re-inspect dance to be solid.
What keeps it out of **L**: zero daemon changes, zero new overlay primitives,
one new extension action, and `navigate` is reused as-is.

### Risks

- **Heuristic false positives.** A normalized or label match could pick the
  wrong param and the app silently accepts it, producing a wrong-but-
  populated field that verifies as `verified` because the *value* matches
  what we sent. Mitigation: only `high`-confidence mappings go into the URL
  by default; low-confidence go to `manualFields`. Residual risk is low.
- **SPA hydration timing.** `settleMs` is a guess; too short → false
  `mismatch`, too long → slow tool. The early-exit poll (re-inspect until all
  mapped fields non-empty) mostly removes this. Default 1200ms is a safe
  middle.
- **Apps that prefill via fragment (`#`) or path segments, not query.** Out
  of scope for v1 — the tool targets `?query=` prefilling only. Such fields
  simply land in `manualFields`; no regression, just no speedup.
- **Known-pattern table staleness.** It's a fallback, kept small and only a
  tie-breaker, so drift is low-impact — the name/id heuristics carry the
  load. Worst case a stale entry is overridden by a higher-ranked real match.
- **Security/UX.** Putting form values in a URL means they appear in browser
  history and the visible address bar. For sensitive values (tokens,
  passwords — detectable via field `type=password` or name heuristics) the
  tool should *refuse* to put them in the URL and route them to
  `manualFields` with `reason:"sensitive field — not placed in URL"`. This is
  a small addition to the mapping phase and worth doing in v1.
