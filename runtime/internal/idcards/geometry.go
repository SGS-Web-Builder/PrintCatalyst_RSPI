// Package idcards implements the local ID Card Studio pipeline: EXIF correction,
// quadrilateral detection, perspective correction, front/back composition, and
// the merchant calibration offsets that align duplex print jobs.
//
// The package is split into pure, side-effect-free modules (geometry, detect,
// layout) and a Service that owns persistence and raster composition. No third-
// party image libraries are introduced: every algorithm runs against the Go
// standard library's image package so the on-premise binary stays self-
// contained.
package idcards

import (
	"errors"
	"math"
)

// Point is a coordinate in image space. Units are floating-point pixels so
// sub-pixel sampling during perspective warp produces correct results.
type Point struct {
	X float64
	Y float64
}

// Quad is a closed polygon of four corner points in image order (top-left,
// top-right, bottom-right, bottom-left). A Quad always has exactly four
// points; use MakeQuad to validate input.
type Quad struct {
	TL Point
	TR Point
	BR Point
	BL Point
}

// MakeQuad validates that every point is finite (no NaN, no Inf) before
// storing them. The order matches the canonical card-corner convention so
// downstream layout code can index TR/BR/BL without worrying about the call
// site reordering the points.
func MakeQuad(tl, tr, br, bl Point) (Quad, error) {
	for _, p := range []Point{tl, tr, br, bl} {
		if !isFinite(p.X) || !isFinite(p.Y) {
			return Quad{}, errors.New("quad corner coordinate must be finite")
		}
	}
	return Quad{TL: tl, TR: tr, BR: br, BL: bl}, nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Points returns the four corners in canonical order. The slice is a fresh
// copy on every call so callers can mutate the result without disturbing the
// Quad value.
func (q Quad) Points() []Point {
	return []Point{q.TL, q.TR, q.BR, q.BL}
}

// Area returns the signed shoelace area of the quad. Positive area means the
// corners traverse the polygon counter-clockwise; negative means clockwise.
// A near-zero area means the quad is degenerate (the four corners are
// collinear or coincident).
func (q Quad) Area() float64 {
	pts := q.Points()
	a := 0.0
	for i := 0; i < 4; i++ {
		j := (i + 1) % 4
		a += pts[i].X*pts[j].Y - pts[j].X*pts[i].Y
	}
	return a / 2
}

// Centroid returns the arithmetic mean of the four corners. Used to rank
// detected quadrilaterals by centrality rather than absolute area alone.
func (q Quad) Centroid() Point {
	return Point{
		X: (q.TL.X + q.TR.X + q.BR.X + q.BL.X) / 4,
		Y: (q.TL.Y + q.TR.Y + q.BR.Y + q.BL.Y) / 4,
	}
}

// Homography is a 3x3 projective transform stored in row-major order. The last
// element (h[8]) is always 1; Homography values are normalised so the bottom
// row reads [h[6], h[7], 1]. Use NewHomography or ComputeHomography to build
// one and ApplyHomography to map a point through it.
type Homography [9]float64

// Identity returns the identity transform. Useful as a sentinel for "no
// perspective correction needed".
func Identity() Homography {
	return Homography{1, 0, 0, 0, 1, 0, 0, 0, 1}
}

// ApplyHomography maps a source point through the homography and returns the
// destination point with the perspective divide applied. The returned values
// are infinite or NaN only if H is degenerate.
func ApplyHomography(h Homography, p Point) Point {
	w := h[6]*p.X + h[7]*p.Y + h[8]
	if w == 0 {
		return Point{X: math.NaN(), Y: math.NaN()}
	}
	return Point{
		X: (h[0]*p.X + h[1]*p.Y + h[2]) / w,
		Y: (h[3]*p.X + h[4]*p.Y + h[5]) / w,
	}
}

// ComputeHomography returns the 3x3 matrix H that maps every destination
// point in `dst` to the corresponding source point in `src`. The order must
// match exactly (TL → TL, TR → TR, BR → BR, BL → BL).
//
// This is the classic Direct Linear Transform (DLT) formulation. We solve
// the 8x8 linear system using Gauss-Jordan elimination with partial pivoting
// so a near-singular configuration (collinear corners) fails loudly rather
// than silently producing a bogus matrix.
func ComputeHomography(src, dst Quad) (Homography, error) {
	type row [8]float64
	const eps = 1e-9
	// For each correspondence (xi, yi) → (Xi, Yi) we build two equations:
	//   [-xi -yi -1  0   0  0  xi*Xi yi*Xi] [h0..h7]ᵀ = -Xi
	//   [ 0   0  0 -xi -yi -1  xi*Yi yi*Yi] [h0..h7]ᵀ = -Yi
	srcPts := src.Points()
	dstPts := dst.Points()
	var A [8]row
	var b [8]float64
	for i := 0; i < 4; i++ {
		sx, sy := srcPts[i].X, srcPts[i].Y
		dx, dy := dstPts[i].X, dstPts[i].Y
		A[2*i] = row{-sx, -sy, -1, 0, 0, 0, sx*dx, sy*dx}
		A[2*i+1] = row{0, 0, 0, -sx, -sy, -1, sx*dy, sy*dy}
		b[2*i] = -dx
		b[2*i+1] = -dy
	}
	// Augmented matrix [A | b].
	M := make([][9]float64, 8)
	for i := 0; i < 8; i++ {
		for j := 0; j < 8; j++ {
			M[i][j] = A[i][j]
		}
		M[i][8] = b[i]
	}
	// Gauss-Jordan elimination with partial pivoting on the first 8 columns.
	for col := 0; col < 8; col++ {
		// Find pivot.
		pivot := col
		maxVal := math.Abs(M[col][col])
		for r := col + 1; r < 8; r++ {
			if v := math.Abs(M[r][col]); v > maxVal {
				maxVal = v
				pivot = r
			}
		}
		if maxVal < eps {
			return Homography{}, errors.New("quadrilaterals are too close to degenerate to compute a homography")
		}
		if pivot != col {
			M[col], M[pivot] = M[pivot], M[col]
		}
		// Normalise pivot row.
		pivotVal := M[col][col]
		for c := col; c <= 8; c++ {
			M[col][c] /= pivotVal
		}
		// Eliminate below.
		for r := col + 1; r < 8; r++ {
			if M[r][col] == 0 {
				continue
			}
			factor := M[r][col]
			for c := col; c <= 8; c++ {
				M[r][c] -= factor * M[col][c]
			}
		}
	}
	// Back-substitute.
	h := [8]float64{}
	for i := 7; i >= 0; i-- {
		sum := M[i][8]
		for j := i + 1; j < 8; j++ {
			sum -= M[i][j] * h[j]
		}
		h[i] = sum
	}
	return Homography{h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7], 1}, nil
}

// ComputeInverseHomography returns H⁻¹. Because the homography is built to
// map destination points back to source points, the caller often needs the
// inverse (mapping source → destination) when laying out a card on a sheet.
func ComputeInverseHomography(h Homography) (Homography, error) {
	inv := [9]float64{}
	const eps = 1e-12
	// Build the augmented 3x6 matrix [H | I].
	M := [3][6]float64{
		{h[0], h[1], h[2], 1, 0, 0},
		{h[3], h[4], h[5], 0, 1, 0},
		{h[6], h[7], h[8], 0, 0, 1},
	}
	for c := 0; c < 3; c++ {
		pivot := c
		maxVal := math.Abs(M[c][c])
		for r := c + 1; r < 3; r++ {
			if v := math.Abs(M[r][c]); v > maxVal {
				maxVal = v
				pivot = r
			}
		}
		if maxVal < eps {
			return Homography{}, errors.New("homography is singular")
		}
		if pivot != c {
			M[c], M[pivot] = M[pivot], M[c]
		}
		pivotVal := M[c][c]
		for k := c; k < 6; k++ {
			M[c][k] /= pivotVal
		}
		for r := 0; r < 3; r++ {
			if r == c {
				continue
			}
			if M[r][c] == 0 {
				continue
			}
			factor := M[r][c]
			for k := c; k < 6; k++ {
				M[r][k] -= factor * M[c][k]
			}
		}
	}
	for r := 0; r < 3; r++ {
		inv[r*3+0] = M[r][3]
		inv[r*3+1] = M[r][4]
		inv[r*3+2] = M[r][5]
	}
	return Homography{inv[0], inv[1], inv[2], inv[3], inv[4], inv[5], inv[6], inv[7], inv[8]}, nil
}

// RectangleF is an axis-aligned rectangle in floating-point units. Width and
// Height are non-negative.
type RectangleF struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// Contains reports whether the point lies inside the rectangle (inclusive on
// the lower edge, exclusive on the upper edge so a rectangle of width 0
// contains no points).
func (r RectangleF) Contains(p Point) bool {
	return p.X >= r.X && p.X < r.X+r.Width && p.Y >= r.Y && p.Y < r.Y+r.Height
}

// QuadFromRect returns a Quad whose corners follow the canonical order for
// the given rectangle. The corner positions are exact, so a QuadFromRect
// input to ComputeHomography produces the identity warp.
func QuadFromRect(r RectangleF) Quad {
	return Quad{
		TL: Point{r.X, r.Y},
		TR: Point{r.X + r.Width, r.Y},
		BR: Point{r.X + r.Width, r.Y + r.Height},
		BL: Point{r.X, r.Y + r.Height},
	}
}
