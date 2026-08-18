package main

import "testing"

func TestSplitFramePath(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"#top_frame", []string{"#top_frame"}},
		{"#top_frame > #csframe", []string{"#top_frame", "#csframe"}},
		{"  #a>#b >#c ", []string{"#a", "#b", "#c"}},
		{"iframe[name=\"x\"] > #y", []string{"iframe[name=\"x\"]", "#y"}},
		{" > > ", []string{}},
	}
	for _, c := range cases {
		got := splitFramePath(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitFramePath(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitFramePath(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestFindNode(t *testing.T) {
	forest := []BiDiContextInfo{
		{Context: "top1", URL: "https://a/", Children: []BiDiContextInfo{
			{Context: "c1", URL: "https://a/f1"},
			{Context: "c2", URL: "https://b/f2", Children: []BiDiContextInfo{
				{Context: "deep", URL: "https://c/deep"},
			}},
		}},
		{Context: "top2", URL: "https://d/"},
	}

	if n := findNode(forest, "top1"); n == nil || n.Context != "top1" {
		t.Errorf("findNode top1 = %v", n)
	}
	if n := findNode(forest, "c2"); n == nil || n.URL != "https://b/f2" {
		t.Errorf("findNode c2 = %v", n)
	}
	// Deeply nested node found via recursion.
	if n := findNode(forest, "deep"); n == nil || n.Context != "deep" {
		t.Errorf("findNode deep = %v", n)
	}
	if n := findNode(forest, "nope"); n != nil {
		t.Errorf("findNode nope = %v, want nil", n)
	}

	// A resolved node exposes its children in tree order (what bidiChildContextInfos maps).
	n := findNode(forest, "top1")
	if len(n.Children) != 2 || n.Children[0].Context != "c1" || n.Children[1].Context != "c2" {
		t.Errorf("top1 children = %v", n.Children)
	}
}

func TestURLOrigin(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://a.example/path?q=1", "https://a.example"},
		{"https://a.example:8443/x", "https://a.example:8443"},
		{"http://h/", "http://h"},
		{"", ""},
		{"about:blank", ""},   // no host
		{"about:srcdoc", ""},  // no host
		{"data:text/html,x", ""},
		{"::::", ""}, // unparseable
	}
	for _, c := range cases {
		if got := urlOrigin(c.in); got != c.want {
			t.Errorf("urlOrigin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUniqueMatch(t *testing.T) {
	kids := []childContext{{id: "a", url: "https://x/1"}, {id: "b", url: "https://y/2"}, {id: "c", url: "https://x/1"}}
	if id, ok := uniqueMatch(kids, func(c childContext) bool { return c.url == "https://y/2" }); !ok || id != "b" {
		t.Errorf("unique y = (%q,%v), want (b,true)", id, ok)
	}
	if _, ok := uniqueMatch(kids, func(c childContext) bool { return c.url == "https://x/1" }); ok {
		t.Error("two x matches should be ambiguous (ok=false)")
	}
	if _, ok := uniqueMatch(kids, func(c childContext) bool { return c.url == "https://none" }); ok {
		t.Error("no match should be ok=false")
	}
}

// TestMatchChildContext is the core qjy guard: identity beats ordinal, and an
// index shifted by a context-less/extra frame must NOT silently pick the wrong
// frame — it must error instead.
func TestMatchChildContext(t *testing.T) {
	kids := []childContext{
		{id: "ctxA", url: "https://a.example/"},
		{id: "ctxB", url: "https://b.example/"},
	}

	// (1) Exact URL match wins even when the ordinal index would point elsewhere.
	//     iframe src is b.example but its document-order idx is 0 (a stale/shifted
	//     index); URL identity must still select ctxB.
	got, err := matchChildContext("#f", kids, frameMatch{URL: "https://b.example/", Idx: 0, Total: 2})
	if err != nil || got != "ctxB" {
		t.Errorf("URL match: got (%q,%v), want (ctxB,nil)", got, err)
	}

	// (2) Origin match when the exact URL differs (post-load redirect keeps origin).
	got, err = matchChildContext("#f", kids, frameMatch{URL: "https://b.example/after-redirect", Idx: 0, Total: 2})
	if err != nil || got != "ctxB" {
		t.Errorf("origin match: got (%q,%v), want (ctxB,nil)", got, err)
	}

	// (3) Ordinal fallback only when counts align and no URL is available.
	got, err = matchChildContext("#f", kids, frameMatch{URL: "", Idx: 1, Total: 2})
	if err != nil || got != "ctxB" {
		t.Errorf("aligned ordinal: got (%q,%v), want (ctxB,nil)", got, err)
	}

	// (4) Count mismatch (3 iframe elements, 2 contexts → one is context-less):
	//     the index is untrustworthy and no URL disambiguates → must ERROR,
	//     never silently return children[idx].
	if got, err := matchChildContext("#f", kids, frameMatch{URL: "", Idx: 1, Total: 3}); err == nil {
		t.Errorf("count mismatch should error, got %q", got)
	}

	// (5) Two same-origin children, no exact URL match: origin is ambiguous, but
	//     the counts align (2 elements, 2 contexts) so the ordinal index is the
	//     trustworthy disambiguator — idx 0 → c1.
	same := []childContext{{id: "c1", url: "https://s/1"}, {id: "c2", url: "https://s/2"}}
	if got, err := matchChildContext("#f", same, frameMatch{URL: "https://s/other", Idx: 0, Total: 2}); err != nil || got != "c1" {
		t.Errorf("aligned ordinal under origin ambiguity: got (%q,%v), want (c1,nil)", got, err)
	}
	// ...but if those same-origin frames come with a count mismatch, ordinal is
	// untrustworthy and it must error rather than guess.
	if _, err := matchChildContext("#f", same, frameMatch{URL: "https://s/other", Idx: 0, Total: 3}); err == nil {
		t.Error("same-origin + count mismatch should error")
	}

	// (6) No children at all → actionable "not loaded yet" error.
	if _, err := matchChildContext("#f", nil, frameMatch{URL: "https://a/", Idx: 0, Total: 1}); err == nil {
		t.Error("empty children should error")
	}
}
