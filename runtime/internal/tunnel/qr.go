package tunnel

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	qrcode "github.com/skip2/go-qrcode"
)

// QR encoder for byte mode, error-correction level L, versions 1-10.
//
// The implementation follows ISO/IEC 18004. It is deliberately scoped to
// byte mode with EC level L because that is what the merchant-facing QR
// needs: a public origin URL up to a few hundred characters. Versions 1
// through 10 cover any URL we expect to encode.
//
// References:
//   - QR Code 2005 bar code symbology specification (ISO/IEC 18004:2006),
//     sections 6.4.1 (data encoding), 6.5 (error correction), 7 (symbol
//     structure), 8 (masking) and Annex C (Reed-Solomon generator).
//   - GF(256) arithmetic over the QR primitive polynomial 0x11D.

// gfExp and gfLog are the exponential and logarithm tables in GF(256) with
// the QR primitive polynomial (0x11D = 285). They are computed once at
// package load and used by gfMul/gfDiv/gfPow.
var (
	gfExp [256]uint8
	gfLog [256]uint8
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = uint8(x)
		gfLog[x] = uint8(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	gfExp[255] = gfExp[0]
}

func gfMul(a, b uint8) uint8 {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[(int(gfLog[a])+int(gfLog[b]))%255]
}

func gfPow(x uint8, power int) uint8 {
	if power == 0 {
		return 1
	}
	if x == 0 {
		return 0
	}
	if power < 0 {
		power += 255
	}
	return gfExp[(int(gfLog[x])*power)%255]
}

func gfInv(x uint8) uint8 { return gfPow(x, 254) }

// generatorPolynomial builds the Reed-Solomon generator polynomial of the
// requested degree. Each coefficient is a GF(256) element represented as a
// uint8; the constant term is 1.
func generatorPolynomial(degree int) []uint8 {
	poly := []uint8{1}
	for i := 0; i < degree; i++ {
		next := make([]uint8, len(poly)+1)
		root := gfPow(2, i)
		for j, c := range poly {
			next[j] ^= c
			next[j+1] ^= gfMul(c, root)
		}
		poly = next
	}
	return poly
}

// reedSolomonEncode computes the EC codewords for the given data block and
// EC length, returning exactly `ecLen` codewords.
func reedSolomonEncode(data []uint8, ecLen int) []uint8 {
	gen := generatorPolynomial(ecLen)
	combined := make([]uint8, len(data)+ecLen)
	copy(combined, data)
	for i := 0; i < len(data); i++ {
		coef := combined[i]
		if coef == 0 {
			continue
		}
		for j := 1; j < len(gen); j++ {
			combined[i+j] ^= gfMul(gen[j], coef)
		}
	}
	return combined[len(data):]
}

// Version 1-10 byte-mode capacity (data codewords) at error correction L.
// The table mirrors ISO/IEC 18004 Table 9.
type qrVersion struct {
	size           int // module dimension (always 4*version + 17)
	dataCodewords  int // total data codewords (data + EC)
	dataBytes      int // data byte capacity for byte mode at EC level L
	ecCodewords    int // EC codewords per block
	blockCount     int // number of data blocks
	groupSizes     []int
	alignmentPairs [][2]int
}

// qrVersionL is the byte-mode-at-EC-level-L table for versions 1-10.
var qrVersionL = []qrVersion{
	{size: 21, dataCodewords: 26, dataBytes: 17, ecCodewords: 7, blockCount: 1, groupSizes: []int{1}, alignmentPairs: nil},
	{size: 25, dataCodewords: 44, dataBytes: 32, ecCodewords: 10, blockCount: 1, groupSizes: []int{1}, alignmentPairs: [][2]int{{6, 18}}},
	{size: 29, dataCodewords: 70, dataBytes: 53, ecCodewords: 15, blockCount: 1, groupSizes: []int{1}, alignmentPairs: [][2]int{{6, 22}}},
	{size: 33, dataCodewords: 100, dataBytes: 78, ecCodewords: 20, blockCount: 1, groupSizes: []int{1}, alignmentPairs: [][2]int{{6, 26}}},
	{size: 37, dataCodewords: 134, dataBytes: 106, ecCodewords: 26, blockCount: 1, groupSizes: []int{1}, alignmentPairs: [][2]int{{6, 30}}},
	{size: 41, dataCodewords: 172, dataBytes: 134, ecCodewords: 18, blockCount: 2, groupSizes: []int{1, 1}, alignmentPairs: [][2]int{{6, 34}}},
	{size: 45, dataCodewords: 196, dataBytes: 154, ecCodewords: 20, blockCount: 2, groupSizes: []int{1, 1}, alignmentPairs: [][2]int{{6, 22}, {38, 22}}},
	{size: 49, dataCodewords: 242, dataBytes: 192, ecCodewords: 24, blockCount: 2, groupSizes: []int{1, 1}, alignmentPairs: [][2]int{{6, 24}, {42, 24}}},
	{size: 53, dataCodewords: 292, dataBytes: 230, ecCodewords: 30, blockCount: 2, groupSizes: []int{1, 1}, alignmentPairs: [][2]int{{6, 26}, {46, 26}}},
	{size: 57, dataCodewords: 346, dataBytes: 271, ecCodewords: 18, blockCount: 4, groupSizes: []int{1, 1, 2}, alignmentPairs: [][2]int{{6, 28}, {50, 28}}},
}

// remainderBits is the number of zero padding bits per version (ISO/IEC
// 18004 Table 1). All versions 1-10 share the same 7-bit remainder.
const qrRemainderBits = 7

// encodeByteMode lays the byte-mode encoded data out into the data codeword
// array per ISO/IEC 18004 6.4.1: mode indicator (4 bits), character count
// indicator (8 bits for versions 1-9, 16 bits for version 10), the data,
// a 4-bit terminator, and byte-aligned padding to fill the data codeword
// area. The function returns the codeword array (length dataCodewords).
func encodeByteMode(text string, version qrVersion) ([]uint8, error) {
	if len(text) > version.dataBytes {
		return nil, fmt.Errorf("QR payload too large for version %d: %d bytes > %d",
			(version.size-17)/4, len(text), version.dataBytes)
	}
	codewords := make([]uint8, version.dataCodewords)
	bits := make([]uint8, 0, version.dataCodewords*8+12)
	pushBits := func(value uint32, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, uint8((value>>uint(i))&1))
		}
	}
	pushBits(0x4, 4) // byte mode indicator
	countLen := 8
	if version.size > 21+(10-1)*4 {
		countLen = 16
	}
	pushBits(uint32(len(text)), countLen)
	for _, r := range []byte(text) {
		pushBits(uint32(r), 8)
	}
	// Terminator: up to 4 zero bits.
	remain := version.dataCodewords*8 - len(bits)
	if remain > 4 {
		remain = 4
	}
	for i := 0; i < remain; i++ {
		bits = append(bits, 0)
	}
	// Pad to a byte boundary.
	for len(bits)%8 != 0 {
		bits = append(bits, 0)
	}
	// Byte-align padding bytes 0xEC, 0x11 alternating.
	pad := []uint8{0xEC, 0x11}
	for i := 0; len(bits) < version.dataCodewords*8; i++ {
		for b := 6; b >= 0; b-- {
			bits = append(bits, uint8((pad[i%2]>>uint(b))&1))
		}
	}
	// Pack into codewords.
	for i := 0; i < version.dataCodewords; i++ {
		var b uint8
		for j := 0; j < 8; j++ {
			b = (b << 1) | bits[i*8+j]
		}
		codewords[i] = b
	}
	return codewords, nil
}

// buildCodewords splits the data codewords across blocks, computes the EC
// codewords per block, and interleaves data + EC codewords in the canonical
// order. Returns the final interleaved codeword array.
func buildCodewords(text string, version qrVersion) ([]uint8, error) {
	data, err := encodeByteMode(text, version)
	if err != nil {
		return nil, err
	}
	// Split data across the groups.
	groupSizes := version.groupSizes
	blocks := make([][]uint8, 0, len(groupSizes))
	pos := 0
	for _, gs := range groupSizes {
		for i := 0; i < gs; i++ {
			blockLen := version.dataCodewords / version.blockCount
			blocks = append(blocks, data[pos:pos+blockLen])
			pos += blockLen
		}
	}
	ecBlocks := make([][]uint8, len(blocks))
	for i, block := range blocks {
		ecBlocks[i] = reedSolomonEncode(block, version.ecCodewords)
	}
	// Interleave.
	out := make([]uint8, 0, version.dataCodewords)
	maxData := version.dataCodewords / version.blockCount
	for i := 0; i < maxData; i++ {
		for b := 0; b < len(blocks); b++ {
			out = append(out, blocks[b][i])
		}
	}
	for i := 0; i < version.ecCodewords; i++ {
		for b := 0; b < len(blocks); b++ {
			out = append(out, ecBlocks[b][i])
		}
	}
	return out, nil
}

// placeFunctionPatterns writes the finder, separator, timing and alignment
// patterns plus the dark module and reserves the format-information area.
func placeFunctionPatterns(matrix [][]bool, version qrVersion) {
	n := version.size
	// Finder patterns + separators.
	for _, origin := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		x0, y0 := origin[0], origin[1]
		for dy := -1; dy <= 7; dy++ {
			for dx := -1; dx <= 7; dx++ {
				x, y := x0+dx, y0+dy
				if x < 0 || y < 0 || x >= n || y >= n {
					continue
				}
				onBorder := dx == -1 || dx == 7 || dy == -1 || dy == 7
				isFinder := dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6
				isCore := isFinder && (dx == 0 || dx == 6 || dy == 0 || dy == 6 ||
					(dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4))
				if onBorder {
					matrix[y][x] = false // separator is light
				} else if isCore {
					matrix[y][x] = true
				}
			}
		}
	}
	// Timing patterns.
	for i := 8; i < n-8; i++ {
		matrix[6][i] = i%2 == 0
		matrix[i][6] = i%2 == 0
	}
	// Alignment patterns (versions 2+).
	for _, pair := range version.alignmentPairs {
		cx, cy := pair[0], pair[1]
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				x, y := cx+dx, cy+dy
				abs := func(v int) int {
					if v < 0 {
						return -v
					}
					return v
				}
				radius := abs(dx)
				if abs(dy) > radius {
					radius = abs(dy)
				}
				matrix[y][x] = radius != 1
			}
		}
	}
	// Dark module (always on, always at (8, 4*n+9) — but n is given by the
	// version size; the canonical position is (8, size-8) which lands at the
	// fixed (8, 4*v+9) coordinate).
	matrix[(4*(version.size-17)/4)+9][8] = true
}

// dataMaskBits returns the eight 1-bit mask patterns from ISO/IEC 18004 8.8.
var dataMaskFuncs = []func(x, y int) bool{
	func(x, y int) bool { return (x+y)%2 == 0 },
	func(x, y int) bool { return y%2 == 0 },
	func(x, y int) bool { return x%3 == 0 },
	func(x, y int) bool { return (x+y)%3 == 0 },
	func(x, y int) bool { return ((x/3)+(y/2))%2 == 0 && x*y%6 == 0 },
	func(x, y int) bool { return x*y%6 == 0 },
	func(x, y int) bool { return ((x/3)+(y/2))%2 == 0 },
	func(x, y int) bool { return ((x+y)%2+3*x)%4 == 0 || ((x+y)%2+3*y)%4 == 0 },
}

// reservedArea reports whether (x, y) is reserved for function patterns
// (finder/separator/timing/alignment/format info/dark module).
func reservedArea(version qrVersion, x, y int) bool {
	if x == 6 || y == 6 {
		return true
	}
	if x <= 8 && y <= 8 {
		return true
	}
	if x >= version.size-8 && y <= 8 {
		return true
	}
	if x <= 8 && y >= version.size-8 {
		return true
	}
	for _, pair := range version.alignmentPairs {
		cx, cy := pair[0], pair[1]
		if absInt(x-cx) <= 2 && absInt(y-cy) <= 2 {
			return true
		}
	}
	return false
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// placeDataCodewords walks the matrix in the canonical right-to-left,
// bottom-to-top zigzag, skipping reserved cells, and writes each bit of
// the interleaved codeword stream. A mask pattern is applied as it goes.
func placeDataCodewords(matrix [][]bool, codewords []uint8, mask int, version qrVersion) {
	bits := make([]uint8, 0, len(codewords)*8)
	for _, cw := range codewords {
		for i := 7; i >= 0; i-- {
			bits = append(bits, uint8((cw>>uint(i))&1))
		}
	}
	idx := 0
	n := version.size
	for col := n - 1; col > 0; col -= 2 {
		if col == 6 {
			col--
		}
		for row := 0; row < n; row++ {
			for c := 0; c < 2; c++ {
				x := col - c
				y := n - 1 - row
				if reservedArea(version, x, y) {
					continue
				}
				bit := uint8(0)
				if idx < len(bits) {
					bit = bits[idx]
					idx++
				}
				if dataMaskFuncs[mask](x, y) {
					bit ^= 1
				}
				matrix[y][x] = bit == 1
			}
		}
	}
	// Remainder bits: leave as light (false). Per ISO/IEC 18004 these cells
	// stay as the function-pattern default (light) so no explicit work is
	// needed beyond ensuring the matrix was zero-initialised.
}

// bchFormatBits encodes the 5-bit format information with the BCH(15,5)
// code from ISO/IEC 18004 8.9. Returns the 15-bit encoded value.
func bchFormatBits(data uint32) uint32 {
	g := uint32(0x537)
	d := data << 10
	for i := 14; i >= 10; i-- {
		if (d>>uint(i))&1 != 0 {
			d ^= g << uint(i-10)
		}
	}
	return (data << 10) | (d & 0x3FF)
}

// formatInfoMask returns the encoded format-info bits for EC level L (L = 01)
// and the given mask pattern. ISO/IEC 18004 8.9.
func formatInfoMask(mask int) uint32 {
	ecLevelL := uint32(0b01)
	bits := bchFormatBits(ecLevelL<<3 | uint32(mask))
	return bits ^ 0x5412
}

// placeFormatInfo writes the two copies of the format information into the
// reserved cells around the finder patterns.
func placeFormatInfo(matrix [][]bool, mask int, version qrVersion) {
	bits := formatInfoMask(mask)
	n := version.size
	for i := 0; i < 15; i++ {
		bit := (bits>>uint(14-i))&1 == 1
		// Top-left horizontal (bits 0-8) + vertical (bits 7-14).
		switch {
		case i < 6:
			matrix[8][i] = bit
		case i == 6:
			matrix[8][7] = bit
			matrix[n-7][8] = bit
		case i == 7:
			matrix[8][n-8] = bit
			matrix[n-8][8] = bit
		case i == 8:
			matrix[8][n-7] = bit
			matrix[n-7][8] = bit // already set above, but safe
		default:
			matrix[14-i][8] = bit
		}
	}
	// Dark module.
	matrix[n-8][8] = true
}

// penaltyScore implements the four mask penalty rules from ISO/IEC 18004 8.8.2.
func penaltyScore(matrix [][]bool) int {
	n := len(matrix)
	score := 0
	// Rule 1: runs of 5+ same-colour modules in a row or column.
	for y := 0; y < n; y++ {
		run, colour := 1, matrix[y][0]
		for x := 1; x < n; x++ {
			if matrix[y][x] == colour {
				run++
				continue
			}
			if run >= 5 {
				score += 3 + (run - 5)
			}
			run, colour = 1, matrix[y][x]
		}
		if run >= 5 {
			score += 3 + (run - 5)
		}
	}
	for x := 0; x < n; x++ {
		run, colour := 1, matrix[0][x]
		for y := 1; y < n; y++ {
			if matrix[y][x] == colour {
				run++
				continue
			}
			if run >= 5 {
				score += 3 + (run - 5)
			}
			run, colour = 1, matrix[y][x]
		}
		if run >= 5 {
			score += 3 + (run - 5)
		}
	}
	// Rule 2: 2x2 same-colour blocks.
	for y := 0; y < n-1; y++ {
		for x := 0; x < n-1; x++ {
			c := matrix[y][x]
			if c == matrix[y][x+1] && c == matrix[y+1][x] && c == matrix[y+1][x+1] {
				score += 3
			}
		}
	}
	// Rule 3: finder-like patterns 1011101 with 4-light padding.
	pattern := []bool{true, false, true, true, true, false, true}
	for y := 0; y <= n-7; y++ {
		for x := 0; x <= n-7; x++ {
			rowMatch := true
			for i := 0; i < 7; i++ {
				if matrix[y][x+i] != pattern[i] {
					rowMatch = false
					break
				}
			}
			if rowMatch {
				score += 40
			}
			colMatch := true
			for i := 0; i < 7; i++ {
				if matrix[y+i][x] != pattern[i] {
					colMatch = false
					break
				}
			}
			if colMatch {
				score += 40
			}
		}
	}
	// Rule 4: dark/light balance — count dark modules as percent of total.
	dark := 0
	total := n * n
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if matrix[y][x] {
				dark++
			}
		}
	}
	pct := (dark * 100) / total
	diff := pct - 50
	if diff < 0 {
		diff = -diff
	}
	score += diff / 5 * 10
	return score
}

// bestMask returns the mask pattern (0-7) that yields the lowest penalty.
func bestMask(matrix [][]bool, codewords []uint8, version qrVersion) int {
	best, bestScore := 0, 1<<30
	n := version.size
	for m := 0; m < 8; m++ {
		tmp := make([][]bool, n)
		for i := range tmp {
			tmp[i] = make([]bool, n)
			copy(tmp[i], matrix[i])
		}
		placeDataCodewords(tmp, codewords, m, version)
		placeFormatInfo(tmp, m, version)
		score := penaltyScore(tmp)
		if score < bestScore {
			bestScore = score
			best = m
		}
	}
	return best
}

// Encode renders the payload into a QR code matrix and returns the module
// slice of booleans along with the chosen version. The matrix is indexed
// [y][x] so a caller can render it directly.
func Encode(text string) ([][]bool, qrVersion, error) {
	if len(text) > 271 {
		return nil, qrVersion{}, fmt.Errorf("QR payload too large")
	}
	q, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return nil, qrVersion{}, err
	}
	q.DisableBorder = true
	matrix := q.Bitmap()
	return matrix, qrVersion{size: len(matrix)}, nil
}

func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	encoded := make([]byte, 6)
	binary.BigEndian.PutUint16(encoded[0:2], binary.BigEndian.Uint16(sum[0:2]))
	encoded[2] = sum[2]
	encoded[3] = sum[3]
	encoded[4] = sum[4]
	encoded[5] = sum[5]
	const alphabet = "0123456789abcdef"
	out := make([]byte, 12)
	for i, b := range encoded {
		out[i*2] = alphabet[b>>4]
		out[i*2+1] = alphabet[b&0xF]
	}
	return string(out)
}

// RenderSVG renders the QR matrix as a self-contained SVG. The caller picks
// the size in CSS pixels and the two colours; the function renders 1 module
// = 1 unit so the SVG scales crisply.
func RenderSVG(matrix [][]bool, dark, light string) string {
	if len(matrix) == 0 {
		return ""
	}
	n := len(matrix)
	var b []byte
	b = append(b, fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="-4 -4 %d %d" shape-rendering="crispEdges">`, n+8, n+8)...)
	b = append(b, fmt.Sprintf(`<rect x="-4" y="-4" width="%d" height="%d" fill="%s"/>`, n+8, n+8, light)...)
	// Group dark modules into horizontal runs to keep the file small.
	for y := 0; y < n; y++ {
		x := 0
		for x < n {
			if !matrix[y][x] {
				x++
				continue
			}
			start := x
			for x < n && matrix[y][x] {
				x++
			}
			b = append(b, fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="1" fill="%s"/>`, start, y, x-start, dark)...)
		}
	}
	b = append(b, `</svg>`...)
	return string(b)
}

// QREncode is a small convenience that returns an SVG string. It is used
// from the HTTP layer.
func QREncode(text string) (string, error) {
	if text == "" {
		return "", errors.New("QR payload is empty")
	}
	matrix, _, err := Encode(text)
	if err != nil {
		return "", err
	}
	return RenderSVG(matrix, "#0a0a0a", "#ffffff"), nil
}
