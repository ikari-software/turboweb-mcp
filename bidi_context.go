package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// contextMap maps integer tab IDs to BiDi browsing context IDs.
// The MCP tool API uses integer tabIds; BiDi uses opaque string context IDs.
// This bridge lets tools keep their existing tabId-based interface.
var (
	tabToContext           = make(map[int]string) // tabId → contextID
	contextToTab           = make(map[string]int) // contextID → tabId
	contextURLs            = make(map[string]string)
	contextMapMu           sync.RWMutex
	nextSyntheticTab       = 100000 // synthetic tab IDs for contexts without a real tab ID
	defaultBrowsingContext string   // first top-level context from last getTree (stable default)
)

// resolveContext translates a tabId (from MCP tool params) to a BiDi browsing context ID.
// If tabId is 0 or nil, returns the default top-level context from the last tree refresh.
func resolveContext(tabId any) (string, error) {
	if err := requireBiDiErr(); err != nil {
		return "", err
	}

	id := tabNum(tabId)

	if err := refreshContextMap(); err != nil {
		return "", fmt.Errorf("context refresh: %w", err)
	}

	tryLookup := func() (string, bool) {
		contextMapMu.RLock()
		defer contextMapMu.RUnlock()
		if id > 0 {
			if ctx, ok := tabToContext[id]; ok {
				return ctx, true
			}
			return "", false
		}
		if defaultBrowsingContext != "" {
			return defaultBrowsingContext, true
		}
		ids := sortedContextIDs()
		if len(ids) > 0 {
			return ids[0], true
		}
		return "", false
	}

	if id > 0 {
		if ctx, ok := tryLookup(); ok {
			return ctx, nil
		}
		_ = syncTabContextFromExtension()
		if ctx, ok := tryLookup(); ok {
			return ctx, nil
		}
		return "", fmt.Errorf("no BiDi context for tab %d (try list_tabs to refresh tab↔context mapping)", id)
	}

	if ctx, ok := tryLookup(); ok {
		return ctx, nil
	}
	return "", fmt.Errorf("no browsing contexts available")
}

// resolveContextDirect returns the first available context without requiring a tabId.
func resolveContextDirect() (string, error) {
	return resolveContext(nil)
}

// refreshContextMap queries browsingContext.getTree and rebuilds the mapping.
func refreshContextMap() error {
	contexts, err := bidiGetTree(context.Background())
	if err != nil {
		return err
	}
	applyContextTree(contexts)
	return nil
}

func applyContextTree(contexts []BiDiContextInfo) {
	contextMapMu.Lock()
	defer contextMapMu.Unlock()

	for k := range contextURLs {
		delete(contextURLs, k)
	}

	validCtx := make(map[string]bool)
	for _, ctx := range contexts {
		validCtx[ctx.Context] = true
		contextURLs[ctx.Context] = ctx.URL
		for _, child := range ctx.Children {
			validCtx[child.Context] = true
			contextURLs[child.Context] = child.URL
		}
	}

	for ctxID := range contextToTab {
		if !validCtx[ctxID] {
			tabID := contextToTab[ctxID]
			delete(tabToContext, tabID)
			delete(contextToTab, ctxID)
		}
	}

	for _, ctx := range contexts {
		if _, exists := contextToTab[ctx.Context]; !exists {
			nextSyntheticTab++
			synth := nextSyntheticTab
			tabToContext[synth] = ctx.Context
			contextToTab[ctx.Context] = synth
		}
	}

	if len(contexts) > 0 {
		defaultBrowsingContext = contexts[0].Context
	} else {
		defaultBrowsingContext = ""
	}
}

func sortedContextIDs() []string {
	out := make([]string, 0, len(contextURLs))
	for id := range contextURLs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// syncTabContextFromExtension pulls list_tabs from the extension and maps tab IDs to BiDi contexts.
func syncTabContextFromExtension() error {
	if getBiDi() == nil {
		return errors.New("BiDi not connected")
	}
	raw, err := send("list_tabs", nil)
	if err != nil {
		return err
	}
	return ingestExtensionTabsForBiDi(raw)
}

// ingestExtensionTabsForBiDi matches extension tabs to top-level BiDi contexts by URL (grouped, order-stable).
func ingestExtensionTabsForBiDi(raw json.RawMessage) error {
	if getBiDi() == nil {
		return nil
	}

	type extTab struct {
		ID     int
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}
	// list_tabs ids are browser-prefixed refs ("zen-3f9a1c2b:42"); BiDi
	// drives a single browser, so only the numeric part matters here.
	var listed []struct {
		ID     any    `json:"id"`
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return err
	}
	tabs := make([]extTab, len(listed))
	for i, t := range listed {
		tabs[i] = extTab{ID: tabNum(t.ID), URL: t.URL, Active: t.Active}
	}

	if err := refreshContextMap(); err != nil {
		return err
	}

	contexts, err := bidiGetTree(context.Background())
	if err != nil {
		return err
	}

	// Group by normalized URL → extension tabs (sorted by id for stable pairing).
	tabByURL := make(map[string][]extTab)
	for _, t := range tabs {
		if t.ID == 0 || t.URL == "" {
			continue
		}
		u := normalizeURL(t.URL)
		if u == "" {
			continue
		}
		tabByURL[u] = append(tabByURL[u], t)
	}
	for u := range tabByURL {
		sort.Slice(tabByURL[u], func(i, j int) bool { return tabByURL[u][i].ID < tabByURL[u][j].ID })
	}

	// Top-level BiDi contexts only (one main frame per tab).
	ctxByURL := make(map[string][]string)
	for _, ctx := range contexts {
		u := normalizeURL(ctx.URL)
		if u == "" {
			continue
		}
		ctxByURL[u] = append(ctxByURL[u], ctx.Context)
	}

	contextMapMu.Lock()
	defer contextMapMu.Unlock()

	for u, tlist := range tabByURL {
		clist := ctxByURL[u]
		n := min(len(tlist), len(clist))
		for i := 0; i < n; i++ {
			registerTabContextLocked(tlist[i].ID, clist[i])
		}
	}

	// Prefer active tab when it still has no mapping: map to first unmatched top-level context with same URL.
	var active extTab
	for _, t := range tabs {
		if t.Active && t.ID != 0 && t.URL != "" {
			active = t
			break
		}
	}
	if active.ID != 0 {
		if _, ok := tabToContext[active.ID]; !ok {
			au := normalizeURL(active.URL)
			for _, ctx := range contexts {
				if normalizeURL(ctx.URL) != au {
					continue
				}
				oldTab, has := contextToTab[ctx.Context]
				// Prefer replacing synthetic tab IDs with the active extension tab.
				if !has || oldTab > 100000 {
					registerTabContextLocked(active.ID, ctx.Context)
					break
				}
			}
		}
	}

	return nil
}

func registerTabContextLocked(tabID int, contextID string) {
	if oldTab, exists := contextToTab[contextID]; exists && oldTab != tabID {
		delete(tabToContext, oldTab)
	}
	tabToContext[tabID] = contextID
	contextToTab[contextID] = tabID
}

// registerTabContext associates a real extension tab ID with a BiDi context ID.
// Called when we can cross-reference extension list_tabs with BiDi getTree by URL.
func registerTabContext(tabID int, contextID string) {
	contextMapMu.Lock()
	defer contextMapMu.Unlock()
	registerTabContextLocked(tabID, contextID)
}

func normalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	parsed.Fragment = ""
	if parsed.Scheme != "" && parsed.Host != "" && parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String()
}

// getContextList returns all known contexts with their URLs and tab IDs.
func getContextList() []map[string]any {
	contextMapMu.RLock()
	defer contextMapMu.RUnlock()

	ids := sortedContextIDs()
	var out []map[string]any
	for _, ctxID := range ids {
		url := contextURLs[ctxID]
		entry := map[string]any{
			"context": ctxID,
			"url":     url,
		}
		if tabID, ok := contextToTab[ctxID]; ok {
			entry["tabId"] = tabID
		}
		out = append(out, entry)
	}
	return out
}

func requireBiDiErr() error {
	_, err := requireBiDi()
	return err
}
