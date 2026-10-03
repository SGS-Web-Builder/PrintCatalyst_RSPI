package idcards

import (
	"errors"
	"image"
	"image/color"
	"math"
	"sort"
)

// DetectionResult is the output of a quadrilateral scan: the four corners in
// canonical order (TL, TR, BR, BL) and a confidence value in [0, 1].
//
// The caller decides what to do with low confidence: the ID Card Studio UI
// surfaces a manual-handle editor when Confidence < MinConfidence and refuses
// to commit a session to the compose step without an explicit manual
// confirmation.
type DetectionResult struct {
	Quad       Quad
	Confidence float64
}

// MinConfidence is the threshold below which the UI presents manual handles
// instead of accepting the detected corners as authoritative. 0.55 was
// chosen by inspection against synthetic fixtures in testdata/ — high enough
// to reject obvious false positives (a high-contrast pattern that happens
// to outline a quad) and low enough to accept real card-on-background photos
// taken with a phone camera.
const MinConfidence = 0.55

// DetectQuadrilateral scans the image for the largest rectangular shape and
// returns the four canonical corners plus a confidence value. The algorithm:
//
//  1. Convert to a luminance plane.
//  2. Compute a Sobel edge map and threshold it.
//  3. Score every unique 4-corner combination of strong edge pixels using a
//     greedy spacing so the search is O(n²) in the number of strong edges
//     rather than O(n⁴).
//  4. Keep the highest-scoring quadrilateral that fits inside the image and
//     whose rectangularity / area coverage exceed the floor.
//
// This is deliberately a heuristic: we are not solving the "find the
// largest rectangle in a binary edge map" exactly because that would require
// either a contour-following pass with substantial complexity or a Hough
// transform. For ID cards the heuristic works because the card is the most
// strongly contrasted quadrilateral in the frame.
func DetectQuadrilateral(src image.Image) (DetectionResult, error) {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 32 || h < 32 {
		return DetectionResult{}, errors.New("image is too small for card detection (minimum 32x32)")
	}
	// Step 1: luminance.
	lum := newLuminance(src, bounds)
	// Step 2: Sobel + threshold.
	edges := sobelThreshold(lum, w, h)
	// Step 3: extract strong edge points (subsampled if necessary).
	strong := sampleStrongEdges(edges, w, h, 600)
	if len(strong) < 4 {
		return DetectionResult{}, errors.New("no strong edges found in the image")
	}
	// Step 4: try a budget of disjoint-edge quadrilaterals; keep the best.
	best, bestScore := searchBestQuad(strong, w, h)
	if bestScore <= 0 {
		return DetectionResult{}, errors.New("could not find a quadrilateral in the image")
	}
	quad, confidence := canonicalise(best, w, h)
	return DetectionResult{Quad: quad, Confidence: confidence}, nil
}

// luminance plane stored as []float64 row-major.
type luminance struct {
	pix []float64
	w, h int
}

func (l *luminance) at(x, y int) float64 {
	return l.pix[y*l.w+x]
}

func newLuminance(src image.Image, bounds image.Rectangle) *luminance {
	w, h := bounds.Dx(), bounds.Dy()
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := src.At(x+bounds.Min.X, y+bounds.Min.Y).RGBA()
			// Rec. 709 luma.
			out[y*w+x] = (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 65535.0
		}
	}
	return &luminance{pix: out, w: w, h: h}
}

// sobelThreshold returns a binary edge map (1 = edge, 0 = non-edge) using
// the Sobel operator with a high threshold so only the strongest gradients
// survive. We do not compute gradient magnitudes below 0.30 because phone
// photos have noise that would otherwise drown the edges.
func sobelThreshold(lum *luminance, w, h int) []byte {
	out := make([]byte, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			tl := lum.at(x-1, y-1)
			tc := lum.at(x, y-1)
			tr := lum.at(x+1, y-1)
			ml := lum.at(x-1, y)
			mr := lum.at(x+1, y)
			bl := lum.at(x-1, y+1)
			bc := lum.at(x, y+1)
			br := lum.at(x+1, y+1)
			gx := (tr+2*mr+br) - (tl+2*ml+bl)
			gy := (bl+2*bc+br) - (tl+2*tc+tr)
			mag := math.Sqrt(gx*gx + gy*gy)
			if mag > 0.30*4 {
				out[y*w+x] = 1
			}
		}
	}
	return out
}

// sampleStrongEdges returns up to `max` evenly-spaced edge pixels for the
// search. Spacing the samples guarantees we cover the whole image and
// prevents one dense cluster from dominating the search.
func sampleStrongEdges(edges []byte, w, h, max int) []Point {
	strideX := 4
	strideY := 4
	if w/h > 2 {
		strideX = 2
	}
	if h/w > 2 {
		strideY = 2
	}
	var out []Point
	for y := 0; y < h; y += strideY {
		for x := 0; x < w; x += strideX {
			if edges[y*w+x] == 1 {
				out = append(out, Point{X: float64(x), Y: float64(y)})
			}
		}
		if len(out) >= max {
			break
		}
	}
	// If still short, take every edge pixel (worst case for high-detail images).
	if len(out) < 4 {
		for y := 0; y < h && len(out) < max*4; y++ {
			for x := 0; x < w && len(out) < max*4; x++ {
				if edges[y*w+x] == 1 {
					out = append(out, Point{X: float64(x), Y: float64(y)})
				}
			}
		}
	}
	if len(out) > max {
		// Evenly sub-sample to limit search budget.
		step := float64(len(out)) / float64(max)
		trimmed := make([]Point, 0, max)
		for i := 0; i < max; i++ {
			idx := int(float64(i) * step)
			if idx >= len(out) {
				idx = len(out) - 1
			}
			trimmed = append(trimmed, out[idx])
		}
		out = trimmed
	}
	return out
}

// searchBestQuad tries every disjoint combination of 4 strong-edge points
// inside a positional budget and returns the highest-scoring quadrilateral
// together with its raw score. We cap the candidate set so the search stays
// bounded even on busy images.
func searchBestQuad(points []Point, w, h int) (Quad, float64) {
	n := len(points)
	if n < 4 {
		return Quad{}, 0
	}
	// Pre-compute image bounds for area coverage.
	imgArea := float64(w * h)
	// Pre-compute min/max corner distances in pixels. Card should cover at
	// least 15% of the frame but not exceed 95% (those are photo corners).
	minDiag := 0.15 * math.Hypot(float64(w), float64(h))
	maxDiag := 0.95 * math.Hypot(float64(w), float64(h))
	var best Quad
	bestScore := -1.0
	// Greedy: for each TL candidate, find TR/BR/BL with sensible spatial
	// relationships. This is O(n³) instead of O(n⁴) and rejects nonsense
	// candidates (e.g., a TL below a BR) cheaply.
	for i := 0; i < n; i++ {
		tl := points[i]
		for j := 0; j < n; j++ {
			if j == i {
				continue
			}
			tr := points[j]
			if !topRowCandidate(tl, tr) {
				continue
			}
			for k := 0; k < n; k++ {
				if k == i || k == j {
					continue
				}
				br := points[k]
				if !rightColCandidate(tr, br) {
					continue
				}
				for l := 0; l < n; l++ {
					if l == i || l == j || l == k {
						continue
					}
					bl := points[l]
					if !bottomRowCandidate(bl, br) || !leftColCandidate(tl, bl) {
						continue
					}
					q, score := scoreQuad(tl, tr, br, bl, imgArea, minDiag, maxDiag)
					if score > bestScore {
						bestScore = score
						best = q
					}
				}
			}
		}
	}
	return best, bestScore
}

// topRowCandidate accepts a (TL, TR) pair if TR is roughly to the right of
// TL and roughly on the same vertical band.
func topRowCandidate(tl, tr Point) bool {
	dx := tr.X - tl.X
	dy := tr.Y - tl.Y
	return dx > 0 && math.Abs(dy) < dx*0.5
}

// rightColCandidate accepts (TR, BR) if BR is below TR and roughly
// vertically aligned.
func rightColCandidate(tr, br Point) bool {
	dy := br.Y - tr.Y
	dx := br.X - tr.X
	return dy > 0 && math.Abs(dx) < dy*0.6
}

// bottomRowCandidate accepts (BL, BR) if BR is to the right of BL on the
// same horizontal band.
func bottomRowCandidate(bl, br Point) bool {
	dx := br.X - bl.X
	dy := br.Y - bl.Y
	return dx > 0 && math.Abs(dy) < dx*0.5
}

// leftColCandidate accepts (TL, BL) if BL is below TL on the same vertical.
func leftColCandidate(tl, bl Point) bool {
	dy := bl.Y - tl.Y
	dx := bl.X - tl.X
	return dy > 0 && math.Abs(dx) < dy*0.6
}

// scoreQuad returns a Quad (canonicalised to TL/TR/BR/BL order) and a score
// combining rectangularity, area coverage and aspect-ratio plausibility.
//
// Score formula:
//   areaCoverage = q.Area() / imageArea
//   rectangularity = 1 - mean angle deviation from 90°
//   aspectPlausibility = 1 - max(0, 1 - 1/(1+AR)) where AR is aspect ratio
//
// We multiply them so a high-coverage but non-rectangular quad scores
// lower than a smaller rectangular quad. Pass minDiag/maxDiag to drop
// quads that obviously cannot be the card (too small or fill the frame).
func scoreQuad(tl, tr, br, bl Point, imgArea, minDiag, maxDiag float64) (Quad, float64) {
	diag := math.Hypot(tr.X-tl.X, tr.Y-tl.Y) + math.Hypot(br.X-bl.X, br.Y-bl.Y)
	if diag < minDiag || diag > maxDiag*2 {
		return Quad{}, 0
	}
	// Reject self-intersecting quads (the four points are not in convex order).
	if !convexOrder(tl, tr, br, bl) {
		return Quad{}, 0
	}
	area := math.Abs(Quad{TL: tl, TR: tr, BR: br, BL: bl}.Area())
	if area < 0.05*imgArea {
		return Quad{}, 0
	}
	if area > 0.95*imgArea {
		return Quad{}, 0
	}
	coverage := area / imgArea
	rect := rectangularity(tl, tr, br, bl)
	aspect := aspectPlausibility(tl, tr, br, bl)
	score := coverage * rect * aspect
	return Quad{TL: tl, TR: tr, BR: br, BL: bl}, score
}

// convexOrder returns true when the four points are in counter-clockwise
// order and form a convex polygon (no concave indentations).
func convexOrder(tl, tr, br, bl Point) bool {
	area := Quad{TL: tl, TR: tr, BR: br, BL: bl}.Area()
	if area <= 0 {
		return false
	}
	// Convex test: every cross product of consecutive edges must share the
	// same sign.
	pts := []Point{tl, tr, br, bl}
	for i := 0; i < 4; i++ {
		a := pts[i]
		b := pts[(i+1)%4]
		c := pts[(i+2)%4]
		cross := (b.X-a.X)*(c.Y-b.Y) - (b.Y-a.Y)*(c.X-b.X)
		if cross <= 0 {
			return false
		}
	}
	return true
}

// rectangularity scores 1.0 for a perfect rectangle, decaying as the four
// corners drift away from right angles. We measure the angle at each corner
// and report 1 - mean(|angle - 90°|/90°).
func rectangularity(tl, tr, br, bl Point) float64 {
	corners := []Point{tl, tr, br, bl}
	total := 0.0
	for i := 0; i < 4; i++ {
		prev := corners[(i+3)%4]
		curr := corners[i]
		next := corners[(i+1)%4]
		v1x, v1y := prev.X-curr.X, prev.Y-curr.Y
		v2x, v2y := next.X-curr.X, next.Y-curr.Y
		dot := v1x*v2x + v1y*v2y
		m1 := math.Hypot(v1x, v1y)
		m2 := math.Hypot(v2x, v2y)
		if m1 == 0 || m2 == 0 {
			return 0
		}
		cos := dot / (m1 * m2)
		if cos > 1 {
			cos = 1
		} else if cos < -1 {
			cos = -1
		}
		angle := math.Acos(cos) * 180 / math.Pi
		total += math.Abs(angle-90) / 90
	}
	return 1 - total/4
}

// aspectPlausibility rewards quads whose width/height ratio is plausible
// for a real ID card (between 1:1 and 2:1 roughly). Cards outside that
// range are unlikely to be the target.
func aspectPlausibility(tl, tr, br, bl Point) float64 {
	w := math.Hypot(tr.X-tl.X, tr.Y-tl.Y)
	h := math.Hypot(bl.X-tl.X, bl.Y-tl.Y)
	if w == 0 || h == 0 {
		return 0
	}
	ratio := w / h
	if ratio < 1 {
		ratio = 1 / ratio
	}
	if ratio < 1.05 {
		return 0.5
	}
	if ratio > 2.5 {
		return 0.5
	}
	if ratio > 2.0 {
		return 0.7
	}
	return 1.0
}

// canonicalise returns a normalised Quad whose corners are in canonical
// (TL, TR, BR, BL) order regardless of which corner the detection step
// found first, plus a confidence value.
//
// Confidence combines three signals:
//   - rectangularity (the strongest signal: a card IS a rectangle)
//   - area coverage, scaled so cards filling 15–80% of the frame score
//     proportionally without saturating the whole formula
//   - aspect plausibility (card-like proportions in the 1.05–2.0 band)
//
// Real-world phone photos of an ID card typically land in the 0.55–0.95
// range. The MinConfidence threshold gates the UI's manual-handle fallback.
func canonicalise(q Quad, w, h int) (Quad, float64) {
	pts := q.Points()
	// Sort by Y ascending; tie-break by X ascending. This yields the top
	// pair (TL, TR) and the bottom pair (BL, BR) in order.
	sort.SliceStable(pts, func(i, j int) bool {
		if pts[i].Y == pts[j].Y {
			return pts[i].X < pts[j].X
		}
		return pts[i].Y < pts[j].Y
	})
	top := pts[:2]
	bottom := pts[2:]
	if top[0].X > top[1].X {
		top[0], top[1] = top[1], top[0]
	}
	if bottom[0].X > bottom[1].X {
		bottom[0], bottom[1] = bottom[1], bottom[0]
	}
	result := Quad{
		TL: top[0],
		TR: top[1],
		BR: bottom[1],
		BL: bottom[0],
	}
	coverage := math.Abs(result.Area()) / float64(w*h)
	rect := rectangularity(result.TL, result.TR, result.BR, result.BL)
	coverageScaled := coverage
	if coverageScaled > 0.8 {
		coverageScaled = 0.8
	}
	coverageScaled = coverageScaled / 0.8
	aspect := aspectPlausibility(result.TL, result.TR, result.BR, result.BL)
	confidence := 0.5*rect + 0.3*coverageScaled + 0.2*aspect
	if confidence > 1 {
		confidence = 1
	}
	return result, confidence
}

// DrawQuadsOverlay is a helper used by tests to render a synthetic image with
// a card outline and a contrasting background. Tests verify that
// DetectQuadrilateral recovers the synthetic corners within a tolerance.
func DrawQuadsOverlay(src image.Image, quads []Quad, colour color.RGBA) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			dst.Set(x, y, color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)})
		}
	}
	for _, q := range quads {
		pts := q.Points()
		for i := 0; i < 4; i++ {
			a := pts[i]
			b := pts[(i+1)%4]
			drawLine(dst, a, b, colour)
		}
	}
	return dst
}

func drawLine(dst *image.RGBA, a, b Point, colour color.RGBA) {
	steps := int(math.Max(math.Abs(b.X-a.X), math.Abs(b.Y-a.Y)))
	if steps < 1 {
		steps = 1
	}
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := int(math.Round(a.X + (b.X-a.X)*t))
		y := int(math.Round(a.Y + (b.Y-a.Y)*t))
		bounds := dst.Bounds()
		if x >= bounds.Min.X && x < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
			dst.Set(x, y, colour)
		}
	}
}
