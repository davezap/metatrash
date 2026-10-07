package service

import (
	"fmt"
	"strconv"
	"strings"
)

// A QR code encoder for the authenticator-app setup page (0.24.0), standard
// library only: byte mode, error correction level M, versions 1–40, the mask
// with the lowest penalty. Rendered as an SVG path so the page needs no image
// and no script. Follows ISO/IEC 18004 as laid out by Project Nayuki's QR Code
// generator.

// eccCodewordsM and eccBlocksM are, per version (index 0 unused), the error
// correction codewords per block and the number of blocks at level M.
var eccCodewordsM = [41]int{-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28}
var eccBlocksM = [41]int{-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49}

const qrFormatBitsM = 0 // level M in the format information

type qrCode struct {
	size     int
	dark     [][]bool
	function [][]bool
}

// qrRawModules is the number of data and error correction bits a version
// holds, after all function patterns.
func qrRawModules(version int) int {
	n := (16*version+128)*version + 64
	if version >= 2 {
		align := version/7 + 2
		n -= (25*align-10)*align - 55
		if version >= 7 {
			n -= 36
		}
	}
	return n
}

func qrDataCodewords(version int) int {
	return qrRawModules(version)/8 - eccCodewordsM[version]*eccBlocksM[version]
}

// encodeQR returns the QR code for data in byte mode at level M, using the
// smallest version that fits.
func encodeQR(data []byte) (*qrCode, error) {
	version := 0
	for v := 1; v <= 40; v++ {
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		if 4+countBits+8*len(data) <= 8*qrDataCodewords(v) {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, fmt.Errorf("data too long for a QR code")
	}
	var bits []bool
	put := func(value, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (value>>i)&1 == 1)
		}
	}
	put(4, 4) // byte mode
	if version >= 10 {
		put(len(data), 16)
	} else {
		put(len(data), 8)
	}
	for _, b := range data {
		put(int(b), 8)
	}
	capacity := 8 * qrDataCodewords(version)
	terminator := min(4, capacity-len(bits))
	put(0, terminator)
	put(0, (8-len(bits)%8)%8)
	for pad := 0xEC; len(bits) < capacity; pad ^= 0xEC ^ 0x11 {
		put(pad, 8)
	}
	codewords := make([]byte, len(bits)/8)
	for i, bit := range bits {
		if bit {
			codewords[i/8] |= 1 << (7 - i%8)
		}
	}
	q := newQR(version)
	q.drawCodewords(qrInterleave(version, codewords))
	best, bestPenalty := 0, -1
	for mask := 0; mask < 8; mask++ {
		q.applyMask(mask)
		q.drawFormat(mask)
		if p := q.penalty(); bestPenalty < 0 || p < bestPenalty {
			best, bestPenalty = mask, p
		}
		q.applyMask(mask) // XOR again to undo
	}
	q.applyMask(best)
	q.drawFormat(best)
	return q, nil
}

func newQR(version int) *qrCode {
	size := version*4 + 17
	q := &qrCode{size: size, dark: make([][]bool, size), function: make([][]bool, size)}
	for y := range size {
		q.dark[y] = make([]bool, size)
		q.function[y] = make([]bool, size)
	}
	for i := range size {
		q.set(6, i, i%2 == 0)
		q.set(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {size - 4, 3}, {3, size - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x >= 0 && x < size && y >= 0 && y < size {
					d := max(abs(dx), abs(dy))
					q.set(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	positions := qrAlignmentPositions(version)
	last := len(positions) - 1
	for i, py := range positions {
		for j, px := range positions {
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue // finder corners
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					q.set(px+dx, py+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	q.drawFormat(0) // reserve the format areas; drawn again after masking
	if version >= 7 {
		rem := version
		for range 12 {
			rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
		}
		bits := version<<12 | rem
		for i := range 18 {
			bit := (bits>>i)&1 == 1
			a, b := size-11+i%3, i/3
			q.set(a, b, bit)
			q.set(b, a, bit)
		}
	}
	return q
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (q *qrCode) set(x, y int, dark bool) {
	q.dark[y][x] = dark
	q.function[y][x] = true
}

func qrAlignmentPositions(version int) []int {
	if version == 1 {
		return nil
	}
	count := version/7 + 2
	step := (version*8 + count*3 + 5) / (count*4 - 4) * 2
	positions := make([]int, count)
	positions[0] = 6
	for i, pos := count-1, version*4+10; i >= 1; i, pos = i-1, pos-step {
		positions[i] = pos
	}
	return positions
}

func (q *qrCode) drawFormat(mask int) {
	data := qrFormatBitsM<<3 | mask
	rem := data
	for range 10 {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>i)&1 == 1 }
	for i := 0; i <= 5; i++ {
		q.set(8, i, bit(i))
	}
	q.set(8, 7, bit(6))
	q.set(8, 8, bit(7))
	q.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		q.set(14-i, 8, bit(i))
	}
	for i := range 8 {
		q.set(q.size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		q.set(8, q.size-15+i, bit(i))
	}
	q.set(8, q.size-8, true)
}

// qrInterleave splits data into blocks, adds Reed–Solomon error correction
// to each and interleaves the result.
func qrInterleave(version int, data []byte) []byte {
	blocks, eccLen := eccBlocksM[version], eccCodewordsM[version]
	raw := qrRawModules(version) / 8
	short := blocks - raw%blocks
	shortLen := raw / blocks
	divisor := rsDivisor(eccLen)
	all := make([][]byte, blocks)
	k := 0
	for i := range blocks {
		n := shortLen - eccLen
		if i >= short {
			n++
		}
		block := append([]byte{}, data[k:k+n]...)
		k += n
		ecc := rsRemainder(block, divisor)
		if i < short {
			block = append(block, 0) // placeholder, skipped when interleaving
		}
		all[i] = append(block, ecc...)
	}
	var out []byte
	for i := range all[0] {
		for j, block := range all {
			if i != shortLen-eccLen || j >= short {
				out = append(out, block[i])
			}
		}
	}
	return out
}

func gfMultiply(x, y byte) byte {
	var z int
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		z ^= int((y>>i)&1) * int(x)
	}
	return byte(z)
}

func rsDivisor(degree int) []byte {
	result := make([]byte, degree)
	result[degree-1] = 1
	root := byte(1)
	for range degree {
		for j := range result {
			result[j] = gfMultiply(result[j], root)
			if j+1 < len(result) {
				result[j] ^= result[j+1]
			}
		}
		root = gfMultiply(root, 2)
	}
	return result
}

func rsRemainder(data, divisor []byte) []byte {
	result := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ result[0]
		copy(result, result[1:])
		result[len(result)-1] = 0
		for i, d := range divisor {
			result[i] ^= gfMultiply(d, factor)
		}
	}
	return result
}

func (q *qrCode) drawCodewords(data []byte) {
	i := 0
	for right := q.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range q.size {
			for j := range 2 {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = q.size - 1 - vert
				}
				if !q.function[y][x] && i < len(data)*8 {
					q.dark[y][x] = (data[i>>3]>>(7-i&7))&1 == 1
					i++
				}
			}
		}
	}
}

func (q *qrCode) applyMask(mask int) {
	for y := range q.size {
		for x := range q.size {
			var invert bool
			switch mask {
			case 0:
				invert = (x+y)%2 == 0
			case 1:
				invert = y%2 == 0
			case 2:
				invert = x%3 == 0
			case 3:
				invert = (x+y)%3 == 0
			case 4:
				invert = (x/3+y/2)%2 == 0
			case 5:
				invert = x*y%2+x*y%3 == 0
			case 6:
				invert = (x*y%2+x*y%3)%2 == 0
			case 7:
				invert = ((x+y)%2+x*y%3)%2 == 0
			}
			if invert && !q.function[y][x] {
				q.dark[y][x] = !q.dark[y][x]
			}
		}
	}
}

// penalty scores a masked symbol with the standard's four rules: runs of one
// colour, 2×2 blocks, finder-like patterns and dark/light balance.
func (q *qrCode) penalty() int {
	n := q.size
	at := func(x, y int, columns bool) bool {
		if columns {
			return q.dark[x][y]
		}
		return q.dark[y][x]
	}
	score, darkCount := 0, 0
	for _, columns := range []bool{false, true} {
		for y := range n {
			run := 1
			var line strings.Builder
			for x := range n {
				d := at(x, y, columns)
				if d {
					line.WriteByte('1')
				} else {
					line.WriteByte('0')
				}
				if x > 0 {
					if d == at(x-1, y, columns) {
						run++
						if run == 5 {
							score += 3
						} else if run > 5 {
							score++
						}
					} else {
						run = 1
					}
				}
			}
			// Finder-like 1:1:3:1:1 with four light modules on one side,
			// counting the light quiet zone around the symbol.
			s := "0000" + line.String() + "0000"
			for i := 0; i+11 <= len(s); i++ {
				if s[i:i+11] == "10111010000" || s[i:i+11] == "00001011101" {
					score += 40
				}
			}
		}
	}
	for y := range n {
		for x := range n {
			if q.dark[y][x] {
				darkCount++
			}
			if x+1 < n && y+1 < n {
				c := q.dark[y][x]
				if c == q.dark[y][x+1] && c == q.dark[y+1][x] && c == q.dark[y+1][x+1] {
					score += 3
				}
			}
		}
	}
	total := n * n
	k := (abs(darkCount*20-total*10)+total-1)/total - 1
	return score + max(k, 0)*10
}

// svg draws the code with a four-module quiet zone, dark on white whatever
// the page's colour scheme, one path for all dark modules.
func (q *qrCode) svg() string {
	var path strings.Builder
	for y := range q.size {
		for x := 0; x < q.size; {
			if !q.dark[y][x] {
				x++
				continue
			}
			start := x
			for x < q.size && q.dark[y][x] {
				x++
			}
			path.WriteString("M" + strconv.Itoa(start+4) + " " + strconv.Itoa(y+4) + "h" + strconv.Itoa(x-start) + "v1h-" + strconv.Itoa(x-start) + "z")
		}
	}
	side := strconv.Itoa(q.size + 8)
	return `<svg class="qr" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ` + side + ` ` + side + `" shape-rendering="crispEdges" role="img" aria-label="QR code for your authenticator app"><rect width="` + side + `" height="` + side + `" fill="#fff"/><path fill="#000" d="` + path.String() + `"/></svg>`
}
