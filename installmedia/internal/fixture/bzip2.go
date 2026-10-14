package fixture

import (
	"math/bits"
	"sort"
)

// Bzip2Compress is a small, slow bzip2 encoder, since the standard
// library only decodes: enough to make UDBZ fixtures that compress/bzip2,
// libbz2 (dmg2img) and 7-Zip all read. It does every stage bzip2 does --
// the initial run-length coding, the Burrows-Wheeler transform,
// move-to-front, the zero-run coding and Huffman coding -- but with one
// fixed Huffman code per block rather than an optimal one.
func Bzip2Compress(data []byte) []byte {
	w := &bitWriter{}
	w.write(24, 'B'<<16|'Z'<<8|'h')
	w.write(8, '9')
	var combined uint32
	// 700,000 bytes grow to at most 875,000 in the initial run-length
	// coding, which is under level 9's 900,000-byte block.
	const blockIn = 700000
	for len(data) > 0 {
		n := min(len(data), blockIn)
		block := data[:n]
		data = data[n:]
		crc := bzipCRC(block)
		combined = (combined<<1 | combined>>31) ^ crc
		writeBlock(w, rle1(block), crc)
	}
	w.write(24, 0x177245)
	w.write(24, 0x385090)
	w.write(32, combined)
	return w.flush()
}

type bitWriter struct {
	buf   []byte
	cur   byte
	nbits uint
}

func (w *bitWriter) write(n uint, v uint32) {
	for i := int(n) - 1; i >= 0; i-- {
		w.cur = w.cur<<1 | byte(v>>uint(i)&1)
		w.nbits++
		if w.nbits == 8 {
			w.buf = append(w.buf, w.cur)
			w.cur, w.nbits = 0, 0
		}
	}
}

func (w *bitWriter) flush() []byte {
	if w.nbits > 0 {
		w.buf = append(w.buf, w.cur<<(8-w.nbits))
		w.cur, w.nbits = 0, 0
	}
	return w.buf
}

// bzipCRC is bzip2's CRC-32: the IEEE polynomial, most significant bit
// first, which hash/crc32 (least significant bit first) does not do.
func bzipCRC(b []byte) uint32 {
	crc := ^uint32(0)
	for _, x := range b {
		crc ^= uint32(x) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return ^crc
}

// rle1 is bzip2's initial run-length coding: a run of 4 to 255 equal
// bytes becomes 4 of them and a count of the rest.
func rle1(b []byte) []byte {
	var out []byte
	for i := 0; i < len(b); {
		j := i
		for j < len(b) && b[j] == b[i] && j-i < 255 {
			j++
		}
		run := j - i
		if run < 4 {
			out = append(out, b[i:j]...)
		} else {
			out = append(out, b[i], b[i], b[i], b[i], byte(run-4))
		}
		i = j
	}
	return out
}

// bwt is the Burrows-Wheeler transform of b: the last column of its
// sorted rotations, and the row the unrotated b sorts to. Rotations are
// sorted by prefix doubling, which is quick enough for fixtures.
func bwt(b []byte) ([]byte, int) {
	n := len(b)
	sa := make([]int, n)
	rank := make([]int, n)
	next := make([]int, n)
	for i := range sa {
		sa[i] = i
		rank[i] = int(b[i])
	}
	for k := 1; ; k <<= 1 {
		less := func(x, y int) bool {
			if rank[x] != rank[y] {
				return rank[x] < rank[y]
			}
			return rank[(x+k)%n] < rank[(y+k)%n]
		}
		sort.Slice(sa, func(i, j int) bool { return less(sa[i], sa[j]) })
		next[sa[0]] = 0
		for i := 1; i < n; i++ {
			next[sa[i]] = next[sa[i-1]]
			if less(sa[i-1], sa[i]) {
				next[sa[i]]++
			}
		}
		copy(rank, next)
		if rank[sa[n-1]] == n-1 || k >= n {
			break
		}
	}
	last := make([]byte, n)
	origPtr := 0
	for i, s := range sa {
		last[i] = b[(s+n-1)%n]
		if s == 0 {
			origPtr = i
		}
	}
	return last, origPtr
}

func writeBlock(w *bitWriter, b []byte, crc uint32) {
	w.write(24, 0x314159)
	w.write(24, 0x265359)
	w.write(32, crc)
	w.write(1, 0) // not randomised
	last, origPtr := bwt(b)
	w.write(24, uint32(origPtr))

	// Which bytes the block uses, sixteen at a time.
	var used [256]bool
	for _, c := range b {
		used[c] = true
	}
	var ranges uint32
	for i := range 16 {
		for j := range 16 {
			if used[i*16+j] {
				ranges |= 1 << (15 - i)
			}
		}
	}
	w.write(16, ranges)
	for i := range 16 {
		if ranges&(1<<(15-i)) == 0 {
			continue
		}
		var set uint32
		for j := range 16 {
			if used[i*16+j] {
				set |= 1 << (15 - j)
			}
		}
		w.write(16, set)
	}
	var seq [256]byte
	nUsed := 0
	for i := range 256 {
		if used[i] {
			seq[i] = byte(nUsed)
			nUsed++
		}
	}

	// Move-to-front, with runs of zeros in bijective base 2 (RUNA = 0,
	// RUNB = 1) and every other index i as symbol i+1.
	mtf := make([]byte, nUsed)
	for i := range mtf {
		mtf[i] = byte(i)
	}
	var syms []int
	zeros := 0
	flush := func() {
		if zeros == 0 {
			return
		}
		r := zeros - 1
		for {
			syms = append(syms, r&1)
			if r < 2 {
				break
			}
			r = (r - 2) / 2
		}
		zeros = 0
	}
	for _, c := range last {
		s := seq[c]
		j := 0
		for mtf[j] != s {
			j++
		}
		if j == 0 {
			zeros++
			continue
		}
		flush()
		copy(mtf[1:j+1], mtf[:j])
		mtf[0] = s
		syms = append(syms, j+1)
	}
	flush()
	syms = append(syms, nUsed+1) // end of block

	// One complete, nearly flat code: the first symbols get k bits and
	// the rest k+1, so the code's Kraft sum is exactly 1.
	alpha := nUsed + 2
	k := bits.Len(uint(alpha)) - 1
	lengths := make([]int, alpha)
	long := 0
	if alpha != 1<<k {
		long = 2 * (alpha - 1<<k)
	}
	for i := range lengths {
		lengths[i] = k
		if i >= alpha-long {
			lengths[i] = k + 1
		}
	}
	codes := make([]uint32, alpha)
	code := uint32(0)
	for l := k; l <= k+1; l++ {
		for s, sl := range lengths {
			if sl == l {
				codes[s] = code
				code++
			}
		}
		code <<= 1
	}

	// Two tables (bzip2's least), both the same code, so every selector
	// is table 0: one 0 bit each in move-to-front unary.
	const groups = 2
	w.write(3, groups)
	selectors := (len(syms) + 49) / 50
	w.write(15, uint32(selectors))
	for range selectors {
		w.write(1, 0)
	}
	for range groups {
		cur := lengths[0]
		w.write(5, uint32(cur))
		for _, l := range lengths {
			for ; cur < l; cur++ {
				w.write(2, 2)
			}
			for ; cur > l; cur-- {
				w.write(2, 3)
			}
			w.write(1, 0)
		}
	}
	for _, s := range syms {
		w.write(uint(lengths[s]), codes[s])
	}
}
