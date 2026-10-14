package fixture

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"
)

// VolumeName is the name HFS gives every volume it makes.
const VolumeName = "Longboat Fixture"

// CatalogFile, named in HFSOptions.Fragment, fragments the catalog file
// itself, so that its own extents spill into the extents overflow file.
const CatalogFile = "\x00catalog"

// File is a file in a volume HFS makes. A Path ending in "/" is an empty
// directory; directories on the way to any path are made as needed.
type File struct {
	Path string
	Data []byte
	// Compressed marks the file HFS+-compressed (UF_COMPRESSED) and
	// leaves its data fork empty, as a compressed file's is: Data is not
	// written anywhere.
	Compressed bool
	// HardLink makes the file a hard link's catalog record (type hlnk,
	// creator hfs+), with no indirect node behind it: enough for a reader
	// to refuse, not for fsck to accept.
	HardLink bool
	// Symlink, if not empty, makes the file a symbolic link to it.
	Symlink string
}

// HFSOptions shape a volume HFS makes. Zero values give 4 KiB blocks and
// 4 KiB B-tree nodes.
type HFSOptions struct {
	BlockSize uint32
	NodeSize  uint16
	// Fragment names files (and CatalogFile) whose blocks are
	// interleaved, one at a time with a free block after each round, so
	// that every block is an extent of its own.
	Fragment []string
	// Journaled sets the journaled attribute; Dirty clears the cleanly
	// unmounted one. No journal is written.
	Journaled, Dirty bool
	// HFSX makes a case-sensitive HFSX volume (signature HX), whose
	// catalog orders names by their UTF-16 code units.
	HFSX bool
}

// HFS-format constants a fixture writes.
const (
	hfsRootParent = 1
	hfsRoot       = 2
	hfsCatalogID  = 4
	hfsFirstUser  = 16

	hfsFolder       = 1
	hfsFile         = 2
	hfsFolderThread = 3
	hfsFileThread   = 4

	hfsHasFolderCount = 0x0010 // kHFSHasFolderCountMask, in a folder record's flags

	hfsUnmounted = 1 << 8
	hfsJournaled = 1 << 13
	hfsNodeFix   = 1 << 31

	hfsLeaf   = 0xff
	hfsIndex  = 0
	hfsHeader = 1

	hfsCatalogMaxKey = 516
	hfsExtentsMaxKey = 10

	hfsDate = 0xe6f50cfd // 2026-10-14, as mkfs.hfsplus wrote one
)

type hfsNode struct {
	name     []uint16
	cnid     uint32
	parent   *hfsNode
	dir      bool
	children []*hfsNode
	file     *File
	body     []byte // the catalog record's body, for its fork data
	data     []byte // the data fork's bytes
	extents  []hfsExtent
}

type hfsExtent struct{ start, count uint32 }

type hfsRecord struct{ key, body []byte }

// HFS makes an HFS+ (or HFSX) volume holding files, laid out as fsck.hfsplus
// accepts it: a catalog sorted by parent and name (ASCII case folded, as
// HFS+ orders ASCII names; binary for HFSX), thread records for every file
// and folder, an allocation bitmap that marks exactly what is used, and an
// alternate volume header. Names in one directory must differ in more than
// case, unless HFSX.
func HFS(files []File, o HFSOptions) []byte {
	bs, ns := int(o.BlockSize), int(o.NodeSize)
	if bs == 0 {
		bs = 4096
	}
	if ns == 0 {
		ns = 4096
	}
	if bs < 512 || bs&(bs-1) != 0 || ns < 512 || ns&(ns-1) != 0 {
		panic(fmt.Sprintf("fixture.HFS: block size %d and node size %d must be powers of two, at least 512", bs, ns))
	}

	root := &hfsNode{name: utf16.Encode([]rune(VolumeName)), cnid: hfsRoot, dir: true}
	for i := range files {
		addHFSFile(root, &files[i], o.HFSX)
	}
	var all []*hfsNode
	var number func(n *hfsNode)
	next := uint32(hfsFirstUser)
	number = func(n *hfsNode) {
		slices.SortFunc(n.children, func(a, b *hfsNode) int { return compareNames(a.name, b.name, o.HFSX) })
		for _, c := range n.children {
			c.cnid = next
			next++
			all = append(all, c)
		}
		for _, c := range n.children {
			number(c)
		}
	}
	number(root)

	// The catalog's records; bodies get their fork data once laid out.
	var recs []hfsRecord
	folders, fileCount := 0, 0
	recs = append(recs, hfsRecord{catalogKey(hfsRootParent, root.name), folderBody(root, o.HFSX)})
	recs = append(recs, hfsRecord{catalogKey(hfsRoot, nil), threadBody(hfsFolderThread, hfsRootParent, root.name)})
	for _, n := range all {
		var body []byte
		if n.dir {
			body = folderBody(n, o.HFSX)
			folders++
			recs = append(recs, hfsRecord{catalogKey(n.cnid, nil), threadBody(hfsFolderThread, n.parent.cnid, n.name)})
		} else {
			body = fileBody(n)
			fileCount++
			recs = append(recs, hfsRecord{catalogKey(n.cnid, nil), threadBody(hfsFileThread, n.parent.cnid, n.name)})
		}
		n.body = body
		recs = append(recs, hfsRecord{catalogKey(n.parent.cnid, n.name), body})
	}
	slices.SortFunc(recs, func(a, b hfsRecord) int { return compareCatalogKeys(a.key, b.key, o.HFSX) })

	blocks := func(n int) int { return (n + bs - 1) / bs }
	treeBlocks := func(used int) (nblocks, nodes int) {
		nblocks = blocks(used * ns)
		return nblocks, nblocks * bs / ns
	}

	// Which forks are fragmented, in Fragment's order.
	byPath := map[string]*hfsNode{}
	for _, n := range all {
		byPath[hfsPath(n)] = n
	}
	catalog := &hfsNode{cnid: hfsCatalogID}
	var fragmented []*hfsNode
	isFragmented := map[*hfsNode]bool{}
	for _, p := range o.Fragment {
		n := catalog
		if p != CatalogFile {
			n = byPath[strings.TrimSuffix(p, "/")]
			if n == nil || n.dir {
				panic("fixture.HFS: Fragment names no file: " + p)
			}
		}
		fragmented = append(fragmented, n)
		isFragmented[n] = true
	}

	catUsed := len(packTree(recs, ns, true, 0, o.HFSX))
	catBlocks, catNodes := treeBlocks(catUsed)

	// The overflow records' keys depend only on block counts, so the
	// extents tree's size is known before any block is placed.
	forkBlocks := func(n *hfsNode) int {
		if n == catalog {
			return catBlocks
		}
		return blocks(len(n.data))
	}
	overflowRecords := func() []hfsRecord {
		var r []hfsRecord
		for _, n := range fragmented {
			nb := forkBlocks(n)
			ext := n.extents
			if len(ext) == 0 {
				ext = make([]hfsExtent, nb) // placeholders: one block each
			}
			for i := 8; i < len(ext); i += 8 {
				body := make([]byte, 64)
				for j, e := range ext[i:min(i+8, len(ext))] {
					binary.BigEndian.PutUint32(body[8*j:], e.start)
					binary.BigEndian.PutUint32(body[8*j+4:], e.count)
				}
				r = append(r, hfsRecord{extentsKey(n.cnid, uint32(i)), body})
			}
		}
		slices.SortFunc(r, compareExtentKeys)
		return r
	}
	extUsed := len(packTree(overflowRecords(), ns, false, 0, o.HFSX))
	extBlocks, extNodes := treeBlocks(extUsed)

	// Lay out the blocks: boot blocks and header, bitmap, extents file,
	// catalog, contiguous files, fragmented forks, alternate header.
	head := blocks(1024 + 512)
	tail := blocks(1024)
	contiguous := 0
	for _, n := range all {
		if !isFragmented[n] {
			contiguous += blocks(len(n.data))
		}
	}
	if !isFragmented[catalog] {
		contiguous += catBlocks
	}
	fragBlocks, rounds := 0, 0
	for _, n := range fragmented {
		fragBlocks += forkBlocks(n)
		rounds = max(rounds, forkBlocks(n))
	}
	bitmapBlocks, total := 1, 0
	for {
		total = head + bitmapBlocks + extBlocks + contiguous + fragBlocks + rounds + tail
		if need := blocks((total + 7) / 8); need > bitmapBlocks {
			bitmapBlocks = need
			continue
		}
		break
	}
	used := make([]bool, total)
	at := 0
	take := func(n int) hfsExtent {
		e := hfsExtent{uint32(at), uint32(n)}
		for i := at; i < at+n; i++ {
			used[i] = true
		}
		at += n
		return e
	}
	take(head)
	bitmap := take(bitmapBlocks)
	extents := take(extBlocks)
	if !isFragmented[catalog] {
		catalog.extents = []hfsExtent{take(catBlocks)}
	}
	for _, n := range all {
		if !isFragmented[n] && len(n.data) > 0 {
			n.extents = []hfsExtent{take(blocks(len(n.data)))}
		}
	}
	for r := 0; r < rounds; r++ {
		for _, n := range fragmented {
			if r < forkBlocks(n) {
				n.extents = append(n.extents, take(1))
			}
		}
		at++ // a free block, so no two blocks of a fork are adjacent
	}
	take(tail)
	if at != total {
		panic(fmt.Sprintf("fixture.HFS: laid out %d blocks of %d", at, total))
	}

	img := make([]byte, total*bs)
	put := func(e []hfsExtent, b []byte) {
		for _, x := range e {
			n := copy(img[int(x.start)*bs:int(x.start+x.count)*bs], b)
			b = b[n:]
		}
	}
	for _, n := range all {
		if !n.dir {
			putFork(n.body[88:], int64(len(n.data)), forkBlocks(n), n.extents)
			put(n.extents, n.data)
		}
	}
	catTree := packTree(recs, ns, true, catNodes, o.HFSX)
	extTree := packTree(overflowRecords(), ns, false, extNodes, o.HFSX)
	put(catalog.extents, slices.Concat(catTree...))
	put([]hfsExtent{extents}, slices.Concat(extTree...))

	free := 0
	bits := make([]byte, bitmapBlocks*bs)
	for i, u := range used {
		if u {
			bits[i/8] |= 0x80 >> (i % 8)
		} else {
			free++
		}
	}
	put([]hfsExtent{bitmap}, bits)

	h := make([]byte, 512)
	if o.HFSX {
		copy(h, "HX\x00\x05")
	} else {
		copy(h, "H+\x00\x04")
	}
	attrs := uint32(hfsUnmounted | hfsNodeFix)
	if o.Dirty {
		attrs &^= hfsUnmounted
	}
	if o.Journaled {
		attrs |= hfsJournaled
	}
	binary.BigEndian.PutUint32(h[4:], attrs)
	copy(h[8:], "10.0")
	if o.Journaled {
		copy(h[8:], "HFSJ")
	}
	for _, off := range []int{16, 20, 28} {
		binary.BigEndian.PutUint32(h[off:], hfsDate)
	}
	binary.BigEndian.PutUint32(h[32:], uint32(fileCount))
	binary.BigEndian.PutUint32(h[36:], uint32(folders))
	binary.BigEndian.PutUint32(h[40:], uint32(bs))
	binary.BigEndian.PutUint32(h[44:], uint32(total))
	binary.BigEndian.PutUint32(h[48:], uint32(free))
	binary.BigEndian.PutUint32(h[52:], uint32(max(slices.Index(used, false), 0))) // the next block to allocate
	binary.BigEndian.PutUint32(h[56:], 0x10000)
	binary.BigEndian.PutUint32(h[60:], 0x10000)
	binary.BigEndian.PutUint32(h[64:], next)
	binary.BigEndian.PutUint64(h[72:], 1) // the MacRoman encoding
	special := func(off int, ext []hfsExtent, nblocks int, size int) {
		putFork(h[off:], int64(size), nblocks, ext)
		binary.BigEndian.PutUint32(h[off+8:], uint32(size)) // clump size
	}
	special(112, []hfsExtent{bitmap}, bitmapBlocks, bitmapBlocks*bs)
	special(192, []hfsExtent{extents}, extBlocks, extNodes*ns)
	special(272, catalog.extents, catBlocks, catNodes*ns)
	copy(img[1024:], h)
	copy(img[len(img)-1024:], h)
	return img
}

// addHFSFile adds f under root, making the directories on its way.
func addHFSFile(root *hfsNode, f *File, hfsx bool) {
	path := f.Path
	isDir := strings.HasSuffix(path, "/")
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	n := root
	for i, p := range parts {
		if p == "" {
			panic("fixture.HFS: an empty name in " + path)
		}
		name := utf16.Encode([]rune(p))
		var child *hfsNode
		for _, c := range n.children {
			if compareNames(c.name, name, hfsx) == 0 {
				child = c
			}
		}
		last := i == len(parts)-1
		if child != nil {
			if last || !child.dir {
				panic("fixture.HFS: " + path + " collides with another file, in a volume where case does not count unless HFSX")
			}
			n = child
			continue
		}
		child = &hfsNode{name: name, parent: n, dir: !last || isDir}
		if last && !isDir {
			child.file = f
			switch {
			case f.Symlink != "":
				child.data = []byte(f.Symlink)
			case f.Compressed, f.HardLink:
			default:
				child.data = f.Data
			}
		}
		n.children = append(n.children, child)
		n = child
	}
}

func hfsPath(n *hfsNode) string {
	var parts []string
	for ; n.parent != nil; n = n.parent {
		parts = append([]string{string(utf16.Decode(n.name))}, parts...)
	}
	return strings.Join(parts, "/")
}

// compareNames orders names as an HFS+ catalog does for ASCII names
// (case folded) or as HFSX does (by code unit).
func compareNames(a, b []uint16, hfsx bool) int {
	fold := func(c uint16) uint16 {
		if !hfsx && c >= 'A' && c <= 'Z' {
			return c + 'a' - 'A'
		}
		return c
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := cmp.Compare(fold(a[i]), fold(b[i])); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

func compareCatalogKeys(a, b []byte, hfsx bool) int {
	if c := cmp.Compare(binary.BigEndian.Uint32(a[2:]), binary.BigEndian.Uint32(b[2:])); c != 0 {
		return c
	}
	return compareNames(keyName(a), keyName(b), hfsx)
}

func keyName(k []byte) []uint16 {
	n := make([]uint16, binary.BigEndian.Uint16(k[6:]))
	for i := range n {
		n[i] = binary.BigEndian.Uint16(k[8+2*i:])
	}
	return n
}

// catalogKey is a catalog key, its length field first.
func catalogKey(parent uint32, name []uint16) []byte {
	k := make([]byte, 8+2*len(name))
	binary.BigEndian.PutUint16(k, uint16(6+2*len(name)))
	binary.BigEndian.PutUint32(k[2:], parent)
	binary.BigEndian.PutUint16(k[6:], uint16(len(name)))
	for i, c := range name {
		binary.BigEndian.PutUint16(k[8+2*i:], c)
	}
	return k
}

// extentsKey is an extents overflow key for a data fork: its length,
// fork type 0x00 and a pad byte, the file ID and the start block.
func extentsKey(cnid, start uint32) []byte {
	k := make([]byte, 12)
	binary.BigEndian.PutUint16(k, hfsExtentsMaxKey)
	binary.BigEndian.PutUint32(k[4:], cnid)
	binary.BigEndian.PutUint32(k[8:], start)
	return k
}

// compareExtentKeys orders extents overflow keys as TN1150 does: by file
// ID, then fork type, then start block -- not in their bytes' order,
// which puts the fork type first.
func compareExtentKeys(a, b hfsRecord) int {
	if c := cmp.Compare(binary.BigEndian.Uint32(a.key[4:]), binary.BigEndian.Uint32(b.key[4:])); c != 0 {
		return c
	}
	if c := cmp.Compare(a.key[2], b.key[2]); c != 0 {
		return c
	}
	return cmp.Compare(binary.BigEndian.Uint32(a.key[8:]), binary.BigEndian.Uint32(b.key[8:]))
}

func threadBody(typ uint16, parent uint32, name []uint16) []byte {
	b := make([]byte, 10+2*len(name))
	binary.BigEndian.PutUint16(b, typ)
	binary.BigEndian.PutUint32(b[4:], parent)
	binary.BigEndian.PutUint16(b[8:], uint16(len(name)))
	for i, c := range name {
		binary.BigEndian.PutUint16(b[10+2*i:], c)
	}
	return b
}

// folderBody is an HFSPlusCatalogFolder. On HFSX its flags carry
// kHFSHasFolderCountMask and its folderCount, at 84 (once reserved),
// counts its child folders: xnu keeps that count on HFSX volumes only,
// and Apple's fsck_hfs requires it there ("HasFolderCount flag needs to
// be set"). An HFS+ folder leaves both zero, which Apple's fsck_hfs
// accepts.
func folderBody(n *hfsNode, hfsx bool) []byte {
	b := make([]byte, 88)
	binary.BigEndian.PutUint16(b, hfsFolder)
	if hfsx {
		binary.BigEndian.PutUint16(b[2:], hfsHasFolderCount)
		folders := 0
		for _, c := range n.children {
			if c.dir {
				folders++
			}
		}
		binary.BigEndian.PutUint32(b[84:], uint32(folders))
	}
	binary.BigEndian.PutUint32(b[4:], uint32(len(n.children)))
	binary.BigEndian.PutUint32(b[8:], n.cnid)
	for _, off := range []int{12, 16, 20, 24} {
		binary.BigEndian.PutUint32(b[off:], hfsDate)
	}
	binary.BigEndian.PutUint16(b[42:], 0o040755)
	return b
}

func fileBody(n *hfsNode) []byte {
	b := make([]byte, 248)
	binary.BigEndian.PutUint16(b, hfsFile)
	binary.BigEndian.PutUint16(b[2:], 0x0002) // its thread record exists
	binary.BigEndian.PutUint32(b[8:], n.cnid)
	for _, off := range []int{12, 16, 20, 24} {
		binary.BigEndian.PutUint32(b[off:], hfsDate)
	}
	mode := uint16(0o100644)
	switch f := n.file; {
	case f.Symlink != "":
		mode = 0o120755
		copy(b[48:], "slnkrhap")
	case f.HardLink:
		mode = 0o100444
		copy(b[48:], "hlnkhfs+")
		binary.BigEndian.PutUint32(b[44:], 1000) // the indirect node it would name
	case f.Compressed:
		b[41] = 0x20 // UF_COMPRESSED
	}
	binary.BigEndian.PutUint16(b[42:], mode)
	return b
}

// putFork writes an HFSPlusForkData: its logical size, its blocks and its
// first 8 extents.
func putFork(b []byte, size int64, nblocks int, ext []hfsExtent) {
	binary.BigEndian.PutUint64(b, uint64(size))
	binary.BigEndian.PutUint32(b[12:], uint32(nblocks))
	for i, e := range ext[:min(8, len(ext))] {
		binary.BigEndian.PutUint32(b[16+8*i:], e.start)
		binary.BigEndian.PutUint32(b[16+8*i+4:], e.count)
	}
}

// packTree lays recs, sorted, out as a B-tree of nodeSize nodes: the
// header node, the leaves, then each index level up to the root. With
// totalNodes 0 it only counts the nodes used; otherwise it gives
// totalNodes nodes, the unused ones zero.
func packTree(recs []hfsRecord, nodeSize int, catalog bool, totalNodes int, hfsx bool) [][]byte {
	type level struct {
		nodes   [][]hfsRecord
		numbers []uint32
	}
	pack := func(rs []hfsRecord) [][]hfsRecord {
		var nodes [][]hfsRecord
		var cur []hfsRecord
		size := 14
		for _, r := range rs {
			n := len(r.key) + len(r.body)
			if len(cur) > 0 && size+n+2*(len(cur)+2) > nodeSize {
				nodes = append(nodes, cur)
				cur, size = nil, 14
			}
			cur = append(cur, r)
			size += n
		}
		if len(cur) > 0 {
			nodes = append(nodes, cur)
		}
		return nodes
	}
	var levels []level
	next := uint32(1)
	for rs := recs; len(rs) > 0; {
		l := level{nodes: pack(rs)}
		for range l.nodes {
			l.numbers = append(l.numbers, next)
			next++
		}
		levels = append(levels, l)
		if len(l.nodes) == 1 {
			break
		}
		rs = nil
		for i, n := range l.nodes {
			rs = append(rs, hfsRecord{n[0].key, binary.BigEndian.AppendUint32(nil, l.numbers[i])})
		}
	}
	used := int(next)
	if totalNodes == 0 {
		return make([][]byte, used)
	}
	if totalNodes < used {
		panic("fixture.HFS: a B-tree outgrew its file")
	}
	if mapBits := (nodeSize - 256) * 8; totalNodes > mapBits {
		panic(fmt.Sprintf("fixture.HFS: %d nodes need a map node; the header's map holds %d", totalNodes, mapBits))
	}
	out := make([][]byte, totalNodes)
	for i := range out {
		out[i] = make([]byte, nodeSize)
	}
	leafRecords := 0
	for li, l := range levels {
		kind := byte(hfsIndex)
		if li == 0 {
			kind = hfsLeaf
		}
		for i, recs := range l.nodes {
			n := out[l.numbers[i]]
			if i > 0 {
				binary.BigEndian.PutUint32(n[4:], l.numbers[i-1])
			}
			if i < len(l.nodes)-1 {
				binary.BigEndian.PutUint32(n[0:], l.numbers[i+1])
			}
			n[8], n[9] = kind, byte(li+1)
			binary.BigEndian.PutUint16(n[10:], uint16(len(recs)))
			off := 14
			for j, r := range recs {
				binary.BigEndian.PutUint16(n[nodeSize-2*(j+1):], uint16(off))
				off += copy(n[off:], r.key)
				off += copy(n[off:], r.body)
			}
			binary.BigEndian.PutUint16(n[nodeSize-2*(len(recs)+1):], uint16(off))
			if li == 0 {
				leafRecords += len(recs)
			}
		}
	}
	hn := out[0]
	hn[8] = hfsHeader
	binary.BigEndian.PutUint16(hn[10:], 3)
	for i, off := range []int{14, 120, 248, nodeSize - 8} {
		binary.BigEndian.PutUint16(hn[nodeSize-2*(i+1):], uint16(off))
	}
	hr := hn[14:]
	if len(levels) > 0 {
		top := levels[len(levels)-1]
		binary.BigEndian.PutUint16(hr[0:], uint16(len(levels)))
		binary.BigEndian.PutUint32(hr[2:], top.numbers[0])
		binary.BigEndian.PutUint32(hr[10:], levels[0].numbers[0])
		binary.BigEndian.PutUint32(hr[14:], levels[0].numbers[len(levels[0].numbers)-1])
	}
	binary.BigEndian.PutUint32(hr[6:], uint32(leafRecords))
	binary.BigEndian.PutUint16(hr[18:], uint16(nodeSize))
	binary.BigEndian.PutUint32(hr[22:], uint32(totalNodes))
	binary.BigEndian.PutUint32(hr[26:], uint32(totalNodes-used))
	binary.BigEndian.PutUint32(hr[32:], uint32(totalNodes*nodeSize))
	if catalog {
		binary.BigEndian.PutUint16(hr[20:], hfsCatalogMaxKey)
		hr[37] = 0xcf // case folding
		if hfsx {
			hr[37] = 0xbc // binary
		}
		binary.BigEndian.PutUint32(hr[38:], 6) // big keys, variable-length index keys
	} else {
		binary.BigEndian.PutUint16(hr[20:], hfsExtentsMaxKey)
		binary.BigEndian.PutUint32(hr[38:], 2) // big keys
	}
	for i := range used {
		hn[248+i/8] |= 0x80 >> (i % 8)
	}
	return out
}
