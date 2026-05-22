package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"sort"

	"github.com/nfnt/resize"
)

// diff.go — the pixel half of screenshot_diff. Pure, no I/O: decode two
// captures, align them, mask out noise sources, run a luma-weighted
// per-pixel compare (the pixelmatch algorithm, reimplemented minimally),
// flood-fill the changed mask into bounding-box regions, and optionally
// paint an annotated thumbnail. Everything here is directly unit-testable
// from diff_test.go — see design/screenshot_diff.md §4.

// DiffRegion is one merged bounding box of changed pixels, in after-image
// coordinates. AreaFrac is the box area as a fraction of the compared
// viewport; Label is a coarse 3×3 grid hint purely for the human reader.
type DiffRegion struct {
	X        int     `json:"x"`
	Y        int     `json:"y"`
	W        int     `json:"w"`
	H        int     `json:"h"`
	AreaFrac float64 `json:"area_frac"`
	Label    string  `json:"label"`
}

// DiffOpts tunes the compare. Zero values are not the defaults — callers
// (handleScreenshotDiff) fill these from the tool params, falling back to
// the constants below.
type DiffOpts struct {
	// Threshold is the per-pixel perceptual delta in [0,1] above which a
	// pixel counts as changed. Default diffThresholdDefault.
	Threshold float64
	// MinScore is the similarity floor in [0,1]: a verdict is "changed"
	// when score < MinScore. Default diffMinScoreDefault.
	MinScore float64
	// Masks are rectangles (in capture pixels, scaled to the compared
	// size by the caller) zeroed in both images before diffing — the
	// resolved `ignore` selectors plus the TurboWeb overlay box.
	Masks []image.Rectangle
	// WantThumb requests an annotated JPEG of the after image with the
	// changed regions outlined.
	WantThumb bool
	// ThumbWidth is the annotated thumbnail width in pixels (default
	// diffThumbWidthDefault).
	ThumbWidth int
}

// DiffResult is the pixel-diff verdict folded into the tool's JSON return.
type DiffResult struct {
	Changed         bool         `json:"changed"`
	Score           float64      `json:"score"`
	ChangedFraction float64      `json:"changed_fraction"`
	Regions         []DiffRegion `json:"regions"`
	RegionCount     int          `json:"region_count"`
	Viewport        DiffViewport `json:"viewport"`
	Scrolled        bool         `json:"scrolled"`
	SizeChanged     bool         `json:"size_changed,omitempty"`
	LikelyAnimation bool         `json:"likely_animation,omitempty"`
	// Thumb is the optional annotated JPEG; nil unless DiffOpts.WantThumb.
	Thumb []byte `json:"-"`
}

// DiffViewport is the compared region's dimensions.
type DiffViewport struct {
	W int `json:"w"`
	H int `json:"h"`
}

const (
	// diffThresholdDefault tolerates JPEG quantization noise (d ≈ 0.01–0.03)
	// while catching real changes (text/colour flips, d ≈ 0.3+).
	diffThresholdDefault = 0.10
	// diffMinScoreDefault — a verdict is "changed" below 99.7% similarity,
	// i.e. when more than 0.3% of compared pixels moved.
	diffMinScoreDefault = 0.997
	// diffThumbWidthDefault is the annotated-thumbnail width.
	diffThumbWidthDefault = 320
	// regionAreaFloorFrac discards connected components smaller than this
	// fraction of the viewport as noise.
	regionAreaFloorFrac = 0.0005
	// regionMergeGap merges region boxes within this many mask cells.
	regionMergeGap = 2
	// maxRegions caps how many boxes are reported, largest first.
	maxRegions = 8
	// maskDownsample is the region-detection mask scale factor: region
	// boxes do not need pixel precision, so flood-fill runs on a 1/N grid.
	maskDownsample = 4
	// caretMaxWidth / caretMinAspect classify a thin tall region as a text
	// caret (narrow, ≥8× taller than wide) so a blinking caret does not
	// register as a page change. The width cap is in full-resolution
	// pixels but region boxes come off the downsampled mask, so a 1–2 px
	// caret upscales to roughly maskDownsample px wide — the cap allows
	// for that quantization.
	caretMaxWidth  = 2 * maskDownsample
	caretMinAspect = 8.0
	// scrollShiftMinFrac — if a vertical-shift cross-correlation explains
	// at least this fraction of changed pixels, the page scrolled and
	// region detection is meaningless.
	scrollShiftMinFrac = 0.6
	// scrollProbeMaxShift caps the vertical offsets the scroll probe tries.
	scrollProbeMaxShift = 64
)

// diffImages is the entry point: decode `before`/`after` (JPEG or PNG, via
// the same image.Decode resize.go relies on), align them, and run the
// perceptual compare. It never performs I/O.
func diffImages(before, after []byte, opts DiffOpts) (DiffResult, error) {
	if opts.Threshold <= 0 {
		opts.Threshold = diffThresholdDefault
	}
	if opts.MinScore <= 0 {
		opts.MinScore = diffMinScoreDefault
	}
	if opts.ThumbWidth <= 0 {
		opts.ThumbWidth = diffThumbWidthDefault
	}

	bImg, _, err := image.Decode(bytes.NewReader(before))
	if err != nil {
		return DiffResult{}, fmt.Errorf("decode before image: %w", err)
	}
	aImg, _, err := image.Decode(bytes.NewReader(after))
	if err != nil {
		return DiffResult{}, fmt.Errorf("decode after image: %w", err)
	}

	// §4.1 alignment: if dimensions differ, scale the smaller to the
	// larger's width and compare the top-aligned overlap. A size change
	// is itself a change, so flag it.
	sizeChanged := false
	bb, ab := bImg.Bounds(), aImg.Bounds()
	if bb.Dx() != ab.Dx() || bb.Dy() != ab.Dy() {
		sizeChanged = true
		bImg, aImg = alignSizes(bImg, aImg)
	}

	bRGBA := toRGBA(bImg)
	aRGBA := toRGBA(aImg)
	w := bRGBA.Bounds().Dx()
	h := bRGBA.Bounds().Dy()

	// §4.1 masking: zero the ignore/overlay rectangles in both images so
	// they cannot diff. Done in-place; the after copy is also used for the
	// thumbnail, which is fine — a masked region carries no signal anyway.
	for _, m := range opts.Masks {
		zeroRect(bRGBA, m)
		zeroRect(aRGBA, m)
	}

	// §4.2 per-pixel changed mask via luma-weighted distance.
	mask := make([]bool, w*h)
	changedCount := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if pixelDelta(bRGBA, aRGBA, x, y) > opts.Threshold {
				mask[i] = true
				changedCount++
			}
		}
	}

	total := w * h
	res := DiffResult{
		Viewport: DiffViewport{W: w, H: h},
	}
	if total == 0 {
		res.Score = 1.0
		return res, nil
	}
	res.ChangedFraction = float64(changedCount) / float64(total)
	res.Score = 1.0 - res.ChangedFraction
	res.SizeChanged = sizeChanged

	// §7 viewport scroll: a global vertical shift lights up nearly every
	// pixel. Detect it and skip region detection — the boxes would be
	// meaningless and the agent should re-baseline.
	if detectScroll(bRGBA, aRGBA, opts.Threshold) {
		res.Scrolled = true
		res.Changed = true
		return res, nil
	}

	// §4.4 region detection: flood-fill the downsampled mask, drop noise,
	// merge, cap, sort largest-first.
	res.Regions = detectRegions(mask, w, h)
	res.RegionCount = len(res.Regions)

	res.Changed = res.Score < opts.MinScore || sizeChanged

	// §7 animation hint: a small steady changed region with no DOM signal
	// is more likely a CSS animation/spinner than a real change. The DOM
	// part is folded in by the caller; here we only flag the pixel shape.
	if res.Changed && !sizeChanged && res.RegionCount == 1 &&
		res.Regions[0].AreaFrac < 0.02 {
		res.LikelyAnimation = true
	}

	if opts.WantThumb {
		thumb, err := annotateThumb(aRGBA, res.Regions, opts.ThumbWidth)
		if err == nil {
			res.Thumb = thumb
		}
	}
	return res, nil
}

// alignSizes scales the narrower image up to the wider one's width (reusing
// nfnt/resize, exactly as resize.go does) so a navigate-induced width change
// still yields a comparable pair. Heights are then cropped to the shorter of
// the two — only the top-aligned overlap is compared (§4.1).
func alignSizes(a, b image.Image) (image.Image, image.Image) {
	aw := a.Bounds().Dx()
	bw := b.Bounds().Dx()
	if aw != bw {
		target := uint(aw)
		if bw > aw {
			target = uint(bw)
		}
		if a.Bounds().Dx() != int(target) {
			ah := uint(a.Bounds().Dy() * int(target) / a.Bounds().Dx())
			a = resize.Resize(target, ah, a, resize.Bilinear)
		}
		if b.Bounds().Dx() != int(target) {
			bh := uint(b.Bounds().Dy() * int(target) / b.Bounds().Dx())
			b = resize.Resize(target, bh, b, resize.Bilinear)
		}
	}
	// Crop both to the shared top-aligned rectangle.
	h := a.Bounds().Dy()
	if b.Bounds().Dy() < h {
		h = b.Bounds().Dy()
	}
	w := a.Bounds().Dx()
	rect := image.Rect(0, 0, w, h)
	return cropRGBA(a, rect), cropRGBA(b, rect)
}

// toRGBA returns img as an *image.RGBA, copying only when necessary so the
// per-pixel loop can index Pix directly instead of paying interface
// dispatch on every At() call.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok && rgba.Bounds().Min == (image.Point{}) {
		return rgba
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// cropRGBA copies the given rectangle of img into a fresh origin-anchored
// RGBA image.
func cropRGBA(img image.Image, rect image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min.Add(rect.Min), draw.Src)
	return dst
}

// zeroRect blacks out a rectangle (clipped to bounds) in an RGBA image.
func zeroRect(img *image.RGBA, r image.Rectangle) {
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	draw.Draw(img, r, image.NewUniform(color.RGBA{}), image.Point{}, draw.Src)
}

// pixelDelta is the luma-weighted per-pixel distance in [0,1]: the pixelmatch
// channel weighting (green dominates perceived brightness) normalized so the
// theoretical maximum is 1.0. A small floor (the caller's Threshold)
// absorbs JPEG quantization noise.
func pixelDelta(a, b *image.RGBA, x, y int) float64 {
	ia := a.PixOffset(x, y)
	ib := b.PixOffset(x, y)
	dr := float64(a.Pix[ia+0]) - float64(b.Pix[ib+0])
	dg := float64(a.Pix[ia+1]) - float64(b.Pix[ib+1])
	db := float64(a.Pix[ia+2]) - float64(b.Pix[ib+2])
	if dr < 0 {
		dr = -dr
	}
	if dg < 0 {
		dg = -dg
	}
	if db < 0 {
		db = -db
	}
	// Weights sum to 1.6; dividing by 1.6·255 maps the worst case to 1.0.
	return (0.6*dr + 0.7*dg + 0.3*db) / (1.6 * 255.0)
}

// detectScroll cross-correlates a few horizontal strips at candidate
// vertical offsets: if shifting `after` up/down by some dy makes most of
// the changed pixels vanish, the page scrolled rather than mutated (§7).
func detectScroll(a, b *image.RGBA, threshold float64) bool {
	w := a.Bounds().Dx()
	h := a.Bounds().Dy()
	if h < scrollProbeMaxShift*2 || w == 0 {
		return false
	}
	// Sample three strips clear of the edges.
	rows := []int{h / 4, h / 2, (3 * h) / 4}
	bestShift := 0
	bestMatch := 0
	for dy := -scrollProbeMaxShift; dy <= scrollProbeMaxShift; dy++ {
		if dy == 0 {
			continue
		}
		match := 0
		samples := 0
		for _, ry := range rows {
			sy := ry + dy
			if sy < 0 || sy >= h {
				continue
			}
			for x := 0; x < w; x += 7 { // sparse sample for speed
				samples++
				if pixelDeltaShift(a, b, x, ry, sy) <= threshold {
					match++
				}
			}
		}
		if samples > 0 && match > bestMatch {
			bestMatch = match
			bestShift = dy
		}
	}
	if bestShift == 0 {
		return false
	}
	// Compare the best shifted match against the unshifted match: a real
	// scroll explains far more pixels when shifted.
	unshifted := 0
	samples := 0
	for _, ry := range rows {
		for x := 0; x < w; x += 7 {
			samples++
			if pixelDelta(a, b, x, ry) <= threshold {
				unshifted++
			}
		}
	}
	if samples == 0 {
		return false
	}
	shiftedFrac := float64(bestMatch) / float64(samples)
	unshiftedFrac := float64(unshifted) / float64(samples)
	return shiftedFrac >= scrollShiftMinFrac && shiftedFrac > unshiftedFrac+0.15
}

// pixelDeltaShift compares a.(x,ay) against b.(x,by) — the strip-shift
// variant used by detectScroll.
func pixelDeltaShift(a, b *image.RGBA, x, ay, by int) float64 {
	ia := a.PixOffset(x, ay)
	ib := b.PixOffset(x, by)
	dr := float64(a.Pix[ia+0]) - float64(b.Pix[ib+0])
	dg := float64(a.Pix[ia+1]) - float64(b.Pix[ib+1])
	db := float64(a.Pix[ia+2]) - float64(b.Pix[ib+2])
	if dr < 0 {
		dr = -dr
	}
	if dg < 0 {
		dg = -dg
	}
	if db < 0 {
		db = -db
	}
	return (0.6*dr + 0.7*dg + 0.3*db) / (1.6 * 255.0)
}

// detectRegions runs connected-components over a downsampled copy of the
// changed mask, turns each component into a bounding box, drops noise-sized
// boxes, merges near-touching ones, caps the count, and sorts by area
// descending (§4.4). Coordinates are returned in full-resolution after-image
// pixels.
func detectRegions(mask []bool, w, h int) []DiffRegion {
	if w == 0 || h == 0 {
		return nil
	}
	ds := maskDownsample
	dw := (w + ds - 1) / ds
	dh := (h + ds - 1) / ds

	// Downsample: a coarse cell is "on" if any covered pixel changed.
	small := make([]bool, dw*dh)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if mask[y*w+x] {
				small[(y/ds)*dw+(x/ds)] = true
			}
		}
	}

	// Flood-fill connected components (4-neighbour) into bounding boxes.
	visited := make([]bool, dw*dh)
	stack := make([]int, 0, 256)
	var boxes []image.Rectangle
	for sy := 0; sy < dh; sy++ {
		for sx := 0; sx < dw; sx++ {
			si := sy*dw + sx
			if !small[si] || visited[si] {
				continue
			}
			minX, minY, maxX, maxY := sx, sy, sx, sy
			stack = stack[:0]
			stack = append(stack, si)
			visited[si] = true
			for len(stack) > 0 {
				ci := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				cx, cy := ci%dw, ci/dw
				if cx < minX {
					minX = cx
				}
				if cx > maxX {
					maxX = cx
				}
				if cy < minY {
					minY = cy
				}
				if cy > maxY {
					maxY = cy
				}
				neighbours := [4][2]int{{cx - 1, cy}, {cx + 1, cy}, {cx, cy - 1}, {cx, cy + 1}}
				for _, n := range neighbours {
					nx, ny := n[0], n[1]
					if nx < 0 || ny < 0 || nx >= dw || ny >= dh {
						continue
					}
					ni := ny*dw + nx
					if small[ni] && !visited[ni] {
						visited[ni] = true
						stack = append(stack, ni)
					}
				}
			}
			// Scale the coarse box back to full resolution.
			boxes = append(boxes, image.Rect(
				minX*ds, minY*ds,
				min((maxX+1)*ds, w), min((maxY+1)*ds, h),
			))
		}
	}

	// Merge boxes that overlap or sit within regionMergeGap·ds pixels.
	gap := regionMergeGap * ds
	boxes = mergeBoxes(boxes, gap)

	// Drop noise-sized and caret-shaped boxes, build regions.
	total := float64(w * h)
	areaFloor := total * regionAreaFloorFrac
	regions := make([]DiffRegion, 0, len(boxes))
	for _, b := range boxes {
		bw, bh := b.Dx(), b.Dy()
		area := float64(bw * bh)
		if area < areaFloor {
			continue
		}
		// Caret heuristic: a thin, tall, on/off stripe is a text caret.
		if bw <= caretMaxWidth && bh > 0 && float64(bh)/float64(max(bw, 1)) >= caretMinAspect {
			continue
		}
		regions = append(regions, DiffRegion{
			X: b.Min.X, Y: b.Min.Y, W: bw, H: bh,
			AreaFrac: area / total,
			Label:    gridLabel(b.Min.X+bw/2, b.Min.Y+bh/2, w, h),
		})
	}

	sort.Slice(regions, func(i, j int) bool {
		return regions[i].AreaFrac > regions[j].AreaFrac
	})
	if len(regions) > maxRegions {
		regions = regions[:maxRegions]
	}
	return regions
}

// mergeBoxes repeatedly unions any two boxes that overlap or come within
// `gap` pixels of each other, until no more merges are possible.
func mergeBoxes(boxes []image.Rectangle, gap int) []image.Rectangle {
	for {
		merged := false
		for i := 0; i < len(boxes); i++ {
			for j := i + 1; j < len(boxes); j++ {
				if boxesNear(boxes[i], boxes[j], gap) {
					boxes[i] = boxes[i].Union(boxes[j])
					boxes = append(boxes[:j], boxes[j+1:]...)
					merged = true
					j--
				}
			}
		}
		if !merged {
			return boxes
		}
	}
}

// boxesNear reports whether a and b overlap or are within `gap` pixels.
func boxesNear(a, b image.Rectangle, gap int) bool {
	return a.Inset(-gap).Overlaps(b)
}

// gridLabel names which cell of a coarse 3×3 grid a point falls into — a
// human-readable hint, never used for logic.
func gridLabel(x, y, w, h int) string {
	if w == 0 || h == 0 {
		return "center"
	}
	col := x * 3 / w
	row := y * 3 / h
	col = clampInt(col, 0, 2)
	row = clampInt(row, 0, 2)
	rows := [3]string{"top", "center", "bottom"}
	cols := [3]string{"left", "center", "right"}
	if rows[row] == "center" && cols[col] == "center" {
		return "center"
	}
	if cols[col] == "center" {
		return rows[row]
	}
	if rows[row] == "center" {
		return cols[col]
	}
	return rows[row] + "-" + cols[col]
}

// annotateThumb paints the changed-region outlines onto a downscaled copy
// of the after image and JPEG-encodes it — the opt-in include_thumb output.
// Reuses bufPool exactly as resize.go's encoder does.
func annotateThumb(after *image.RGBA, regions []DiffRegion, width int) ([]byte, error) {
	w := after.Bounds().Dx()
	h := after.Bounds().Dy()
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("empty image")
	}
	scale := float64(width) / float64(w)
	thumbH := int(float64(h) * scale)
	if thumbH < 1 {
		thumbH = 1
	}
	thumb := toRGBA(resize.Resize(uint(width), uint(thumbH), after, resize.Bilinear))

	// Outline each region in opaque red, 2 px thick.
	red := color.RGBA{R: 255, G: 32, B: 32, A: 255}
	for _, rg := range regions {
		rx := int(float64(rg.X) * scale)
		ry := int(float64(rg.Y) * scale)
		rw := int(float64(rg.W) * scale)
		rh := int(float64(rg.H) * scale)
		drawRectOutline(thumb, image.Rect(rx, ry, rx+rw, ry+rh), red, 2)
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := jpeg.Encode(buf, thumb, &jpeg.Options{Quality: 70}); err != nil {
		return nil, fmt.Errorf("encode thumb: %w", err)
	}
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out, nil
}

// drawRectOutline strokes a `thick`-pixel rectangle border in the given
// colour, clipped to the image.
func drawRectOutline(img *image.RGBA, r image.Rectangle, c color.RGBA, thick int) {
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	for t := 0; t < thick; t++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if r.Min.Y+t < r.Max.Y {
				img.SetRGBA(x, r.Min.Y+t, c)
			}
			if r.Max.Y-1-t >= r.Min.Y {
				img.SetRGBA(x, r.Max.Y-1-t, c)
			}
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			if r.Min.X+t < r.Max.X {
				img.SetRGBA(r.Min.X+t, y, c)
			}
			if r.Max.X-1-t >= r.Min.X {
				img.SetRGBA(r.Max.X-1-t, y, c)
			}
		}
	}
}

// clampInt bounds v to [lo,hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
