package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// screenshot_diff_handler_test.go — exercises handleScreenshotDiff end to
// end against the mock browser transport: mint → verify, the JSON return
// shape, the expired-baseline error, and the include_thumb image block (§8).

// b64PNG renders a solid w×h image and returns base64 PNG — what the mock
// `screenshot` action hands back.
func b64PNG(t *testing.T, w, h int, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// b64PatchedPNG is b64PNG with a filled rectangle painted in.
func b64PatchedPNG(t *testing.T, w, h int, bg color.RGBA, patch image.Rectangle, pc color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (image.Point{x, y}).In(patch) {
				img.SetRGBA(x, y, pc)
			} else {
				img.SetRGBA(x, y, bg)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// callDiff invokes handleScreenshotDiff with the given args.
func callDiff(t *testing.T, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := handleScreenshotDiff(context.Background(), req)
	if err != nil {
		t.Fatalf("handleScreenshotDiff: %v", err)
	}
	return res
}

func TestScreenshotDiff_MintBaseline(t *testing.T) {
	theBaselineCache = newBaselineCache()
	bg := color.RGBA{R: 200, G: 200, B: 200, A: 255}
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		switch msg["action"] {
		case "screenshot":
			return map[string]any{"result": map[string]any{
				"base64": b64PNG(t, 400, 300, bg), "width": 400, "height": 300,
				"url": "https://example.com",
			}}
		case "dom_mutations_mark":
			return map[string]any{"result": map[string]any{"token": "{}"}}
		}
		return map[string]any{"result": map[string]any{}}
	}, func() {
		res := callDiff(t, map[string]any{"intent": "mark before"})
		var out struct {
			BaselineID string `json:"baseline_id"`
			Minted     bool   `json:"minted"`
		}
		if err := json.Unmarshal([]byte(extractText(t, res)), &out); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !out.Minted || out.BaselineID == "" {
			t.Errorf("mint call: minted=%v id=%q, want true/non-empty", out.Minted, out.BaselineID)
		}
		if _, ok := theBaselineCache.get(out.BaselineID); !ok {
			t.Error("minted baseline not stored in cache")
		}
	})
}

func TestScreenshotDiff_VerifyChanged(t *testing.T) {
	theBaselineCache = newBaselineCache()
	bg := color.RGBA{R: 240, G: 240, B: 240, A: 255}
	// Seed a baseline directly so the test controls the before image.
	before := func() []byte {
		s := b64PNG(t, 400, 300, bg)
		d, _ := base64.StdEncoding.DecodeString(s)
		return d
	}()
	id := theBaselineCache.put(baselineEntry{image: before, url: "https://example.com", domMark: "{}"})

	after := b64PatchedPNG(t, 400, 300, bg, image.Rect(80, 60, 240, 200), color.RGBA{R: 10, G: 10, B: 10, A: 255})
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		switch msg["action"] {
		case "screenshot":
			return map[string]any{"result": map[string]any{
				"base64": after, "width": 400, "height": 300, "url": "https://example.com",
			}}
		case "screenshot_diff_meta":
			return map[string]any{"result": map[string]any{
				"masks": []any{},
				"dom": map[string]any{"mutations": 3, "added": 1, "attrs": 2},
				"url": "https://example.com",
			}}
		}
		return map[string]any{"result": map[string]any{}}
	}, func() {
		res := callDiff(t, map[string]any{"intent": "verify", "baseline_id": id})
		var out struct {
			Changed     bool          `json:"changed"`
			Score       float64       `json:"score"`
			RegionCount int           `json:"region_count"`
			Regions     []DiffRegion  `json:"regions"`
			BaselineID  string        `json:"baseline_id"`
			DOM         struct{ Mutations int `json:"mutations"` } `json:"dom"`
		}
		if err := json.Unmarshal([]byte(extractText(t, res)), &out); err != nil {
			t.Fatalf("parse: %v\nraw: %s", err, extractText(t, res))
		}
		if !out.Changed {
			t.Error("verify: changed = false, want true")
		}
		if out.RegionCount != 1 {
			t.Errorf("verify: region_count = %d, want 1", out.RegionCount)
		}
		if out.DOM.Mutations != 3 {
			t.Errorf("verify: dom.mutations = %d, want 3", out.DOM.Mutations)
		}
		if out.BaselineID == "" || out.BaselineID == id {
			t.Errorf("verify: expected a fresh baseline_id, got %q", out.BaselineID)
		}
		if out.Score >= 1.0 {
			t.Errorf("verify: score = %v, want < 1.0", out.Score)
		}
	})
}

func TestScreenshotDiff_FastPathSkipsCapture(t *testing.T) {
	theBaselineCache = newBaselineCache()
	d, _ := base64.StdEncoding.DecodeString(b64PNG(t, 400, 300, color.RGBA{A: 255}))
	id := theBaselineCache.put(baselineEntry{image: d, url: "https://example.com", domMark: "{}"})

	captured := false
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		switch msg["action"] {
		case "screenshot":
			captured = true
			return map[string]any{"result": map[string]any{"base64": b64PNG(t, 400, 300, color.RGBA{A: 255}), "width": 400, "height": 300}}
		case "screenshot_diff_meta":
			return map[string]any{"result": map[string]any{
				"masks": []any{},
				"dom":   map[string]any{"mutations": 0},
				"url":   "https://example.com",
			}}
		}
		return map[string]any{"result": map[string]any{}}
	}, func() {
		res := callDiff(t, map[string]any{"intent": "verify no-op", "baseline_id": id})
		var out struct {
			Changed      bool `json:"changed"`
			PixelSkipped bool `json:"pixel_skipped"`
		}
		if err := json.Unmarshal([]byte(extractText(t, res)), &out); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if out.Changed {
			t.Error("fast path: changed = true, want false for zero DOM mutations")
		}
		if !out.PixelSkipped {
			t.Error("fast path: pixel_skipped = false, want true")
		}
		if captured {
			t.Error("fast path took an after-capture; it should have been skipped")
		}
	})
}

func TestScreenshotDiff_ExpiredBaseline(t *testing.T) {
	theBaselineCache = newBaselineCache()
	withMockBrowser(t, echoHandler, func() {
		res := callDiff(t, map[string]any{"intent": "verify", "baseline_id": "b_notreal"})
		if !res.IsError {
			t.Error("expired/unknown baseline_id should return a tool error")
		}
	})
}

func TestScreenshotDiff_IncludeThumb(t *testing.T) {
	theBaselineCache = newBaselineCache()
	bg := color.RGBA{R: 240, G: 240, B: 240, A: 255}
	d, _ := base64.StdEncoding.DecodeString(b64PNG(t, 400, 300, bg))
	id := theBaselineCache.put(baselineEntry{image: d, url: "https://example.com", domMark: "{}"})
	after := b64PatchedPNG(t, 400, 300, bg, image.Rect(80, 60, 240, 200), color.RGBA{A: 255})

	withMockBrowser(t, func(msg map[string]any) map[string]any {
		switch msg["action"] {
		case "screenshot":
			return map[string]any{"result": map[string]any{"base64": after, "width": 400, "height": 300, "url": "https://example.com"}}
		case "screenshot_diff_meta":
			return map[string]any{"result": map[string]any{"masks": []any{}, "dom": map[string]any{"mutations": 1}, "url": "https://example.com"}}
		}
		return map[string]any{"result": map[string]any{}}
	}, func() {
		res := callDiff(t, map[string]any{"intent": "verify", "baseline_id": id, "include_thumb": true})
		if len(res.Content) != 2 {
			t.Fatalf("include_thumb: %d content blocks, want 2 (text + image)", len(res.Content))
		}
		// Second block must be an image.
		b, _ := json.Marshal(res.Content[1])
		var block struct{ Type string }
		json.Unmarshal(b, &block)
		if block.Type != "image" {
			t.Errorf("include_thumb: second block type = %q, want image", block.Type)
		}
	})
}
