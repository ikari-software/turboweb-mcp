package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// diff_test.go — pure-function coverage for the screenshot_diff pixel
// algorithm, mirroring resize_test.go's style. See design §8.

// solidPNG paints a w×h image filled with c and PNG-encodes it (lossless,
// so identical-input tests are not muddied by JPEG noise).
func solidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
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
	return buf.Bytes()
}

// patchedPNG starts from a solid background and paints `patch` filled with
// `pc` into it — used to plant a known changed region.
func patchedPNG(t *testing.T, w, h int, bg color.RGBA, patch image.Rectangle, pc color.RGBA) []byte {
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
	return buf.Bytes()
}

// gradientJPEG encodes a horizontal gradient as JPEG at the given quality —
// re-encoding the same source twice produces a realistic noise pair.
func gradientJPEG(t *testing.T, w, h, quality int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 100, A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestDiffImages_Identical(t *testing.T) {
	img := solidPNG(t, 400, 300, color.RGBA{R: 200, G: 150, B: 100, A: 255})
	res, err := diffImages(img, img, DiffOpts{})
	if err != nil {
		t.Fatalf("diffImages: %v", err)
	}
	if res.Changed {
		t.Errorf("identical images: changed = true, want false")
	}
	if res.Score != 1.0 {
		t.Errorf("identical images: score = %v, want 1.0", res.Score)
	}
	if res.ChangedFraction != 0 {
		t.Errorf("identical images: changed_fraction = %v, want 0", res.ChangedFraction)
	}
	if len(res.Regions) != 0 {
		t.Errorf("identical images: %d regions, want 0", len(res.Regions))
	}
}

func TestDiffImages_KnownRegion(t *testing.T) {
	bg := color.RGBA{R: 240, G: 240, B: 240, A: 255}
	patch := image.Rect(100, 80, 220, 180) // 120×100 box
	before := solidPNG(t, 400, 300, bg)
	after := patchedPNG(t, 400, 300, bg, patch, color.RGBA{R: 20, G: 20, B: 20, A: 255})

	res, err := diffImages(before, after, DiffOpts{})
	if err != nil {
		t.Fatalf("diffImages: %v", err)
	}
	if !res.Changed {
		t.Fatalf("known region change: changed = false, want true")
	}
	if res.RegionCount != 1 {
		t.Fatalf("known region change: %d regions, want 1: %+v", res.RegionCount, res.Regions)
	}
	r := res.Regions[0]
	// The detected box should contain the planted patch and not wildly
	// exceed it. Region detection runs on a downsampled mask, so allow a
	// maskDownsample-sized tolerance on each edge.
	tol := maskDownsample
	if r.X > patch.Min.X || r.Y > patch.Min.Y ||
		r.X+r.W < patch.Max.X || r.Y+r.H < patch.Max.Y {
		t.Errorf("region %+v does not cover patch %v", r, patch)
	}
	if r.X < patch.Min.X-tol || r.Y < patch.Min.Y-tol {
		t.Errorf("region %+v overshoots patch %v origin", r, patch)
	}
	// changed_fraction should be ≈ patch area / total.
	wantFrac := float64(patch.Dx()*patch.Dy()) / float64(400*300)
	if res.ChangedFraction < wantFrac*0.8 || res.ChangedFraction > wantFrac*1.2 {
		t.Errorf("changed_fraction = %v, want ≈ %v", res.ChangedFraction, wantFrac)
	}
}

func TestDiffImages_JPEGNoiseIgnored(t *testing.T) {
	// The SAME source encoded twice at q70 differs in thousands of pixels
	// but is visually identical — the threshold floor must absorb it.
	a := gradientJPEG(t, 600, 400, 70)
	b := gradientJPEG(t, 600, 400, 70)
	res, err := diffImages(a, b, DiffOpts{})
	if err != nil {
		t.Fatalf("diffImages: %v", err)
	}
	if res.Changed {
		t.Errorf("JPEG re-encode noise: changed = true (score %v), want false", res.Score)
	}
}

func TestDiffImages_CaretDropped(t *testing.T) {
	// A 2×40 px tall thin stripe (a text caret) must not register as a
	// region — the caret heuristic drops thin tall components.
	bg := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	caret := image.Rect(300, 100, 302, 140)
	before := solidPNG(t, 600, 400, bg)
	after := patchedPNG(t, 600, 400, bg, caret, color.RGBA{A: 255})

	res, err := diffImages(before, after, DiffOpts{})
	if err != nil {
		t.Fatalf("diffImages: %v", err)
	}
	if res.RegionCount != 0 {
		t.Errorf("caret stripe produced %d regions, want 0: %+v", res.RegionCount, res.Regions)
	}
	if res.Changed {
		t.Errorf("a blinking caret must not register as a change")
	}
}

func TestDiffImages_MismatchedDimensions(t *testing.T) {
	before := solidPNG(t, 400, 300, color.RGBA{R: 100, G: 100, B: 100, A: 255})
	after := solidPNG(t, 800, 600, color.RGBA{R: 100, G: 100, B: 100, A: 255})
	res, err := diffImages(before, after, DiffOpts{})
	if err != nil {
		t.Fatalf("diffImages must not panic on size mismatch: %v", err)
	}
	if !res.SizeChanged {
		t.Errorf("size mismatch: size_changed = false, want true")
	}
	if !res.Changed {
		t.Errorf("size mismatch: changed = false, want true")
	}
}

func TestDiffImages_Masking(t *testing.T) {
	// A change entirely inside a mask rectangle must be ignored.
	bg := color.RGBA{R: 240, G: 240, B: 240, A: 255}
	patch := image.Rect(50, 50, 150, 150)
	before := solidPNG(t, 400, 300, bg)
	after := patchedPNG(t, 400, 300, bg, patch, color.RGBA{R: 10, G: 10, B: 10, A: 255})

	masked, err := diffImages(before, after, DiffOpts{
		Masks: []image.Rectangle{image.Rect(40, 40, 160, 160)},
	})
	if err != nil {
		t.Fatalf("diffImages: %v", err)
	}
	if masked.Changed {
		t.Errorf("change fully inside mask: changed = true, want false")
	}
}

func TestDiffImages_RegionMerge(t *testing.T) {
	// Two adjacent rectangles within the merge gap should fuse into one box.
	bg := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	before := encodePNG(t, img)
	dark := color.RGBA{A: 255}
	for y := 100; y < 160; y++ {
		for x := 100; x < 150; x++ {
			img.SetRGBA(x, y, dark)
		}
		for x := 154; x < 210; x++ { // 4 px gap — within regionMergeGap·ds
			img.SetRGBA(x, y, dark)
		}
	}
	after := encodePNG(t, img)

	res, err := diffImages(before, after, DiffOpts{})
	if err != nil {
		t.Fatalf("diffImages: %v", err)
	}
	if res.RegionCount != 1 {
		t.Errorf("two adjacent rects: %d regions, want 1 merged: %+v", res.RegionCount, res.Regions)
	}
}

func TestDiffImages_Thumbnail(t *testing.T) {
	bg := color.RGBA{R: 240, G: 240, B: 240, A: 255}
	after := patchedPNG(t, 600, 400, bg, image.Rect(100, 100, 300, 250), color.RGBA{A: 255})
	before := solidPNG(t, 600, 400, bg)

	res, err := diffImages(before, after, DiffOpts{WantThumb: true, ThumbWidth: 320})
	if err != nil {
		t.Fatalf("diffImages: %v", err)
	}
	if res.Thumb == nil {
		t.Fatal("WantThumb set but Thumb is nil")
	}
	img, err := jpeg.Decode(bytes.NewReader(res.Thumb))
	if err != nil {
		t.Fatalf("thumb is not valid JPEG: %v", err)
	}
	if img.Bounds().Dx() != 320 {
		t.Errorf("thumb width = %d, want 320", img.Bounds().Dx())
	}
}

func TestDiffImages_InvalidInput(t *testing.T) {
	good := solidPNG(t, 100, 100, color.RGBA{A: 255})
	if _, err := diffImages([]byte("not an image"), good, DiffOpts{}); err == nil {
		t.Error("expected error for invalid before image")
	}
	if _, err := diffImages(good, []byte("not an image"), DiffOpts{}); err == nil {
		t.Error("expected error for invalid after image")
	}
}

func TestGridLabel(t *testing.T) {
	cases := []struct {
		x, y int
		want string
	}{
		{10, 10, "top-left"},
		{640, 10, "top"},
		{1270, 10, "top-right"},
		{640, 400, "center"},
		{10, 790, "bottom-left"},
		{1270, 790, "bottom-right"},
	}
	for _, c := range cases {
		if got := gridLabel(c.x, c.y, 1280, 800); got != c.want {
			t.Errorf("gridLabel(%d,%d) = %q, want %q", c.x, c.y, got, c.want)
		}
	}
}

func TestDiffImages_ScrollDetected(t *testing.T) {
	// A page scroll shifts every row vertically. Build a before image with
	// distinct per-row content (a vertical gradient), then shift every row
	// down ~20px for the after image — detectScroll should catch the global
	// vertical shift and set Scrolled.
	const w, h, shift = 200, 400, 20
	before := image.NewRGBA(image.Rect(0, 0, w, h))
	after := image.NewRGBA(image.Rect(0, 0, w, h))
	// rowColor gives each row a distinct colour so a vertical shift is
	// unambiguous to the cross-correlation probe.
	rowColor := func(y int) color.RGBA {
		return color.RGBA{R: uint8(y % 256), G: uint8((y * 3) % 256), B: 128, A: 255}
	}
	for y := 0; y < h; y++ {
		bc := rowColor(y)
		// after.row(y) carries the colour of before.row(y-shift): the whole
		// page has scrolled down by `shift` pixels.
		ac := rowColor(((y - shift) + h) % h)
		for x := 0; x < w; x++ {
			before.SetRGBA(x, y, bc)
			after.SetRGBA(x, y, ac)
		}
	}

	res, err := diffImages(encodePNG(t, before), encodePNG(t, after), DiffOpts{})
	if err != nil {
		t.Fatalf("diffImages: %v", err)
	}
	if !res.Scrolled {
		t.Errorf("a global vertical shift must set Scrolled=true (score %v)", res.Score)
	}

	// Negative case: a small localized change must NOT be mistaken for a
	// scroll — only a handful of rows differ, so detectScroll stays quiet.
	bg := color.RGBA{R: 240, G: 240, B: 240, A: 255}
	tallEnough := patchedPNG(t, w, h, bg, image.Rect(60, 150, 140, 210), color.RGBA{A: 255})
	solid := solidPNG(t, w, h, bg)
	res2, err := diffImages(solid, tallEnough, DiffOpts{})
	if err != nil {
		t.Fatalf("diffImages (localized): %v", err)
	}
	if res2.Scrolled {
		t.Errorf("a small localized change must not set Scrolled")
	}
}

// encodePNG is a small helper for tests that mutate an *image.RGBA in place.
func encodePNG(t *testing.T, img *image.RGBA) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}
