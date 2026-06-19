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

	// A resolved node exposes its children in tree order (what bidiChildContexts maps).
	n := findNode(forest, "top1")
	if len(n.Children) != 2 || n.Children[0].Context != "c1" || n.Children[1].Context != "c2" {
		t.Errorf("top1 children = %v", n.Children)
	}
}
