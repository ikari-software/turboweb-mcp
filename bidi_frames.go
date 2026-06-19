package main

import (
	"context"
	"encoding/json"
	"fmt"
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
// in the CURRENT context to find which iframe (by document order among all
// iframe/frame elements) the selector matches, then take that child context by
// the same index from browsingContext.getTree. The iframe ELEMENT always lives
// in the parent context, which BiDi can always evaluate in regardless of the
// child's origin — that's why this reaches cross-origin frames. We also sum the
// per-frame content origin so a top-viewport coordinate can be translated to
// the child's local frame for input.

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
		idx, ox, oy, ferr := frameIndexAndOrigin(ctx, ctxID, seg)
		if ferr != nil {
			return "", 0, 0, ferr
		}
		children, cerr := bidiChildContexts(ctx, ctxID)
		if cerr != nil {
			return "", 0, 0, cerr
		}
		if idx < 0 || idx >= len(children) {
			return "", 0, 0, fmt.Errorf(
				"frame %q: selector matched iframe #%d but its browsing-context tree "+
					"reports %d child frame(s) — the frame may not have loaded yet",
				seg, idx, len(children))
		}
		ctxID = children[idx]
		offX += ox
		offY += oy
	}
	return ctxID, offX, offY, nil
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

// frameIndexAndOrigin evaluates in contextID to locate the iframe/frame matched
// by selector. It returns the element's index among ALL iframe/frame elements
// in document order (so it lines up with getTree's child ordering) plus the
// content viewport origin (frame rect + border + padding) for coordinate
// translation.
func frameIndexAndOrigin(ctx context.Context, contextID, selector string) (int, float64, float64, error) {
	selJSON, _ := json.Marshal(selector)
	expr := fmt.Sprintf(`JSON.stringify((()=>{`+
		`const all=[...document.querySelectorAll('iframe,frame')];`+
		`const e=document.querySelector(%s);`+
		`if(!e)return {idx:-1};`+
		`const r=e.getBoundingClientRect();const cs=getComputedStyle(e);`+
		`return {idx:all.indexOf(e),`+
		`ox:r.left+(parseFloat(cs.borderLeftWidth)||0)+(parseFloat(cs.paddingLeft)||0),`+
		`oy:r.top+(parseFloat(cs.borderTopWidth)||0)+(parseFloat(cs.paddingTop)||0)};`+
		`})())`, string(selJSON))

	var res struct {
		Idx int     `json:"idx"`
		Ox  float64 `json:"ox"`
		Oy  float64 `json:"oy"`
	}
	if err := bidiEvaluateJSON(ctx, contextID, expr, &res); err != nil {
		return -1, 0, 0, fmt.Errorf("frame %q: %w", selector, err)
	}
	if res.Idx < 0 {
		return -1, 0, 0, fmt.Errorf("frame selector %q matched no <iframe>/<frame> in the target document", selector)
	}
	return res.Idx, res.Ox, res.Oy, nil
}

// bidiChildContexts returns the immediate child browsing-context ids of ctxID,
// in tree order (which matches DOM iframe order in practice).
func bidiChildContexts(ctx context.Context, contextID string) ([]string, error) {
	tree, err := bidiGetTreeFrom(ctx, contextID, 1)
	if err != nil {
		return nil, err
	}
	node := findNode(tree, contextID)
	if node == nil {
		return nil, fmt.Errorf("browsing context %q not found in tree", contextID)
	}
	out := make([]string, len(node.Children))
	for i, ch := range node.Children {
		out[i] = ch.Context
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
