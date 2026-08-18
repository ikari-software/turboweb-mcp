package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Cross-origin frame targeting for the cdp_* tools.
//
// In WebDriver BiDi every iframe — same- OR cross-origin — is its own browsing
// context with its own id, and input.performActions / script.evaluate dispatch
// against whatever context you name (coordinates are relative to that context's
// own viewport). So piercing a cross-origin frame is just "address the child
// context" — no special OOPIF handling needed.
//
// The hard part is mapping the caller's `frame` spec to a child context id.
// A spec is one of:
//   - a framePath: a ">"-separated list of CSS selectors, each resolving an
//     <iframe>/<frame> within the previous frame's document (the same form
//     list_frames emits, e.g. "#top_frame > #csframe"); or
//   - a raw BiDi context id (escape hatch / advanced use).
//
// framePath resolution walks segment by segment. For each segment we evaluate
// in the CURRENT context to find the matching iframe — its resolved src URL,
// its index among all iframe/frame elements, and the total element count — then
// pick the child browsing context from browsingContext.getTree. We prefer
// matching by URL/origin (robust) and only fall back to the document-order
// index when it is provably aligned with the context tree; see matchChildContext
// for why ordinal-only mapping is unsafe. The iframe ELEMENT always lives in the
// parent context, which BiDi can always evaluate in regardless of the child's
// origin — that's why this reaches cross-origin frames.
//
// Coordinate translation: we sum each hop's content-viewport origin (frame rect
// + border + padding). Each origin is read with getBoundingClientRect in ITS
// parent context, i.e. that context's post-scroll viewport, so the sum
// telescopes to the leaf viewport's origin in top-viewport coordinates even when
// intermediate ancestors are scrolled. BiDi pointer coordinates are viewport-
// relative to the target context, so a leaf-local coordinate is simply
// (topCoord - summedOrigin) — the child document's OWN scroll must NOT be added,
// as it is already reflected in where its elements sit within its viewport.

// childContext pairs a child browsing-context id with its current URL, as
// reported by browsingContext.getTree.
type childContext struct {
	id  string
	url string
}

// frameMatch is the parent-context view of one iframe/frame element: its
// resolved src URL, its index among all iframe/frame elements in document order,
// the total count of such elements, and its content-viewport origin.
type frameMatch struct {
	URL   string  `json:"url"`
	Idx   int     `json:"idx"`
	Total int     `json:"total"`
	Ox    float64 `json:"ox"`
	Oy    float64 `json:"oy"`
}

// resolveFrameContext maps (tabId, frameSpec) to a target browsing context id
// and the cumulative offset of that frame's content viewport within the top
// viewport. An empty frameSpec resolves to the tab's top-level context with a
// zero offset (i.e. identical to resolveContext) so callers stay unchanged.
func resolveFrameContext(ctx context.Context, tabId any, frameSpec string) (ctxID string, offX, offY float64, err error) {
	ctxID, err = resolveContext(tabId)
	if err != nil {
		return "", 0, 0, err
	}
	frameSpec = strings.TrimSpace(frameSpec)
	if frameSpec == "" {
		return ctxID, 0, 0, nil
	}

	// Escape hatch: a raw context id that exists somewhere in the tree. Offset
	// is unknown in this form, so coordinates are treated as frame-local.
	if found, ok := findContextInTree(ctx, frameSpec); ok {
		return found, 0, 0, nil
	}

	for _, seg := range splitFramePath(frameSpec) {
		fm, ferr := frameElementInfo(ctx, ctxID, seg)
		if ferr != nil {
			return "", 0, 0, ferr
		}
		children, cerr := bidiChildContextInfos(ctx, ctxID)
		if cerr != nil {
			return "", 0, 0, cerr
		}
		child, merr := matchChildContext(seg, children, fm)
		if merr != nil {
			return "", 0, 0, merr
		}
		ctxID = child
		offX += fm.Ox
		offY += fm.Oy
	}
	return ctxID, offX, offY, nil
}

// matchChildContext picks the child browsing context that owns the iframe
// described by fm. Ordinal document-order mapping (children[idx]) is unsafe on
// its own: browsingContext.getTree lists only iframes that HAVE a browsing
// context, so a not-yet-loaded / context-less iframe shifts the index and an
// SPA adding or removing frames between the element lookup and getTree (TOCTOU)
// shifts it too — either way an in-range-but-shifted index silently dispatches
// real input into the WRONG cross-origin frame. So we prefer identity:
//
//  1. exactly one child whose URL equals the iframe's resolved src — robust
//     against index shift and frame churn;
//  2. exactly one child whose ORIGIN matches — handles post-load redirects that
//     preserve the origin;
//  3. the document-order index, but ONLY when the element count equals the
//     child-context count (proving no context-less/extra frame shifted it).
//
// When none of these disambiguate we fail loudly rather than guess, pointing the
// caller at a more specific selector.
func matchChildContext(seg string, children []childContext, fm frameMatch) (string, error) {
	if len(children) == 0 {
		return "", fmt.Errorf(
			"frame %q: selector matched an iframe but its browsing-context tree reports "+
				"no child frames — the frame may not have loaded yet", seg)
	}

	// (1) Unique exact-URL match.
	if fm.URL != "" {
		if id, ok := uniqueMatch(children, func(c childContext) bool { return c.url == fm.URL }); ok {
			return id, nil
		}
		// (2) Unique same-origin match.
		if origin := urlOrigin(fm.URL); origin != "" {
			if id, ok := uniqueMatch(children, func(c childContext) bool { return urlOrigin(c.url) == origin }); ok {
				return id, nil
			}
		}
	}

	// (3) Ordinal fallback — only trustworthy when the element count matches the
	// context count, i.e. every iframe element has exactly one browsing context
	// and nothing shifted the index.
	if fm.Total == len(children) && fm.Idx >= 0 && fm.Idx < len(children) {
		return children[fm.Idx].id, nil
	}

	return "", fmt.Errorf(
		"frame %q: could not map selector to a cross-origin browsing context unambiguously "+
			"(iframe URL %q; %d iframe element(s) vs %d child context(s)) — the frame may still be "+
			"loading, or several frames share a URL/origin; pass a more specific frame selector or "+
			"target the frame by a raw BiDi context id",
		seg, fm.URL, fm.Total, len(children))
}

// uniqueMatch returns the single child satisfying pred, or ok=false if zero or
// more than one match (ambiguous).
func uniqueMatch(children []childContext, pred func(childContext) bool) (string, bool) {
	found := ""
	n := 0
	for _, c := range children {
		if pred(c) {
			found = c.id
			n++
		}
	}
	if n == 1 {
		return found, true
	}
	return "", false
}

// urlOrigin returns scheme://host[:port] for u, or "" if u is empty/unparseable
// or has no host (about:blank, about:srcdoc, data: — none usable for matching).
func urlOrigin(u string) string {
	if u == "" {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return ""
	}
	return p.Scheme + "://" + p.Host
}

// splitFramePath splits "#a > #b" into ["#a", "#b"], trimming and dropping empties.
func splitFramePath(spec string) []string {
	parts := strings.Split(spec, ">")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// frameElementInfo evaluates in contextID to locate the iframe/frame matched by
// selector. It returns the element's resolved src URL, its index among ALL
// iframe/frame elements in document order, the total count of such elements (so
// the caller can tell whether the index is aligned with the context tree), and
// the content viewport origin (frame rect + border + padding) for coordinate
// translation. NOTE: the ox/oy rect+border+padding formula below mirrors
// frameContentOrigin() in extension/content.js (the synthetic path). Keep the
// two in sync.
func frameElementInfo(ctx context.Context, contextID, selector string) (frameMatch, error) {
	selJSON, _ := json.Marshal(selector)
	expr := fmt.Sprintf(`JSON.stringify((()=>{`+
		`const all=[...document.querySelectorAll('iframe,frame')];`+
		`const e=document.querySelector(%s);`+
		`if(!e)return {idx:-1,total:all.length};`+
		`const r=e.getBoundingClientRect();const cs=getComputedStyle(e);`+
		`let url='';try{const s=e.getAttribute('src');if(s)url=new URL(s,document.baseURI).href;}catch(_){}`+
		`return {idx:all.indexOf(e),total:all.length,url,`+
		`ox:r.left+(parseFloat(cs.borderLeftWidth)||0)+(parseFloat(cs.paddingLeft)||0),`+
		`oy:r.top+(parseFloat(cs.borderTopWidth)||0)+(parseFloat(cs.paddingTop)||0)};`+
		`})())`, string(selJSON))

	var res frameMatch
	if err := bidiEvaluateJSON(ctx, contextID, expr, &res); err != nil {
		return frameMatch{}, fmt.Errorf("frame %q: %w", selector, err)
	}
	if res.Idx < 0 {
		return frameMatch{}, fmt.Errorf("frame selector %q matched no <iframe>/<frame> in the target document", selector)
	}
	return res, nil
}

// bidiChildContextInfos returns the immediate child browsing contexts of ctxID
// (id + current URL), in tree order (which matches DOM iframe order in practice).
func bidiChildContextInfos(ctx context.Context, contextID string) ([]childContext, error) {
	tree, err := bidiGetTreeFrom(ctx, contextID, 1)
	if err != nil {
		return nil, err
	}
	node := findNode(tree, contextID)
	if node == nil {
		return nil, fmt.Errorf("browsing context %q not found in tree", contextID)
	}
	out := make([]childContext, len(node.Children))
	for i, ch := range node.Children {
		out[i] = childContext{id: ch.Context, url: ch.URL}
	}
	return out, nil
}

// findContextInTree reports whether id names an existing browsing context.
func findContextInTree(ctx context.Context, id string) (string, bool) {
	tree, err := bidiGetTreeFrom(ctx, "", 0)
	if err != nil {
		return "", false
	}
	if node := findNode(tree, id); node != nil {
		return node.Context, true
	}
	return "", false
}

// findNode walks a context forest depth-first for the node with the given id.
func findNode(forest []BiDiContextInfo, id string) *BiDiContextInfo {
	for i := range forest {
		if forest[i].Context == id {
			return &forest[i]
		}
		if n := findNode(forest[i].Children, id); n != nil {
			return n
		}
	}
	return nil
}
