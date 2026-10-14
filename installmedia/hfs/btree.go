package hfs

import (
	"encoding/binary"
	"fmt"
)

// maxDepth is the deepest an HFS+ B-tree grows (kMaxTreeDepth in Apple's
// B-tree code).
const maxDepth = 16

// B-tree node kinds and header attributes (TN1150).
const (
	kindLeaf     = -1
	kindIndex    = 0
	kindHeader   = 1
	bigKeys      = 1 << 1
	varIndexKeys = 1 << 2
)

// btree is one of a volume's B-trees: its file and its header record's
// fields, each checked against that file.
type btree struct {
	what         string
	f            *fork
	nodeSize     int
	depth        int
	root         uint32
	totalNodes   uint32
	maxKeyLen    int
	minKeyLen    int
	varIndexKeys bool
}

type node struct {
	num    uint32
	fLink  uint32
	kind   int8
	height int
	recs   [][]byte
}

// openBTree reads the header record of the B-tree in f. Its keys are at
// least minKeyLen bytes, not counting their length field.
func openBTree(f *fork, what string, minKeyLen int) (*btree, error) {
	const headerNode = 14 + 106 // a node descriptor and a header record
	if f.size < headerNode {
		return nil, corrupt("the %s file is %d bytes, too small for a B-tree's header node", what, f.size)
	}
	b := make([]byte, headerNode)
	if _, err := f.ReadAt(b, 0); err != nil {
		return nil, fmt.Errorf("reading the %s B-tree's header node: %w", what, err)
	}
	if k := int8(b[8]); k != kindHeader {
		return nil, corrupt("the %s file's first node is of kind %d, not a B-tree header node", what, k)
	}
	h := b[14:]
	t := &btree{
		what:       what,
		f:          f,
		depth:      int(binary.BigEndian.Uint16(h[0:])),
		root:       binary.BigEndian.Uint32(h[2:]),
		nodeSize:   int(binary.BigEndian.Uint16(h[18:])),
		maxKeyLen:  int(binary.BigEndian.Uint16(h[20:])),
		totalNodes: binary.BigEndian.Uint32(h[22:]),
		minKeyLen:  minKeyLen,
	}
	if ns := t.nodeSize; ns < 512 || ns > 32768 || ns&(ns-1) != 0 {
		return nil, corrupt("the %s B-tree's node size, %d, is not a power of two from 512 to 32768", what, ns)
	}
	if uint64(t.totalNodes)*uint64(t.nodeSize) > uint64(f.size) {
		return nil, corrupt("the %s B-tree claims %d nodes of %d bytes, past the end of its %d-byte file", what, t.totalNodes, t.nodeSize, f.size)
	}
	if t.depth > maxDepth {
		return nil, corrupt("the %s B-tree's depth, %d, is past the %d levels an HFS+ B-tree reaches", what, t.depth, maxDepth)
	}
	switch {
	case t.depth == 0 && t.root != 0:
		return nil, corrupt("the %s B-tree has a depth of 0 but a root node, %d", what, t.root)
	case t.depth > 0 && (t.root == 0 || t.root >= t.totalNodes):
		return nil, corrupt("the %s B-tree's root node, %d, is not among its nodes 1 to %d", what, t.root, int64(t.totalNodes)-1)
	}
	attrs := binary.BigEndian.Uint32(h[38:])
	if attrs&bigKeys == 0 {
		return nil, corrupt("the %s B-tree's keys are not HFS+'s 16-bit-length keys (attributes %#x)", what, attrs)
	}
	t.varIndexKeys = attrs&varIndexKeys != 0
	if !t.varIndexKeys && t.maxKeyLen < minKeyLen {
		return nil, corrupt("the %s B-tree's maximum key length, %d, is shorter than its keys' %d bytes", what, t.maxKeyLen, minKeyLen)
	}
	return t, nil
}

// readNode reads node n and splits it into its records, checking every
// record offset against the node.
func (t *btree) readNode(n uint32) (*node, error) {
	if n == 0 || n >= t.totalNodes {
		return nil, corrupt("the %s B-tree links to node %d, which is not among its nodes 1 to %d", t.what, n, int64(t.totalNodes)-1)
	}
	b := make([]byte, t.nodeSize)
	if _, err := t.f.ReadAt(b, int64(n)*int64(t.nodeSize)); err != nil {
		return nil, fmt.Errorf("reading node %d of the %s B-tree: %w", n, t.what, err)
	}
	nd := &node{num: n, fLink: binary.BigEndian.Uint32(b), kind: int8(b[8]), height: int(b[9])}
	count := int(binary.BigEndian.Uint16(b[10:]))
	limit := t.nodeSize - 2*(count+1) // where the record offsets begin
	if limit < 14 {
		return nil, corrupt("the %s B-tree's node %d claims %d records, more than its %d bytes hold", t.what, n, count, t.nodeSize)
	}
	offset := func(i int) int { return int(binary.BigEndian.Uint16(b[t.nodeSize-2*(i+1):])) }
	prev := 14
	for i := 0; i <= count; i++ {
		o := offset(i)
		if o < prev || (i > 0 && o == prev) || o > limit {
			return nil, corrupt("the %s B-tree's node %d has a record offset of %d for record %d, out of order or outside its records' bytes, 14 to %d", t.what, n, o, i, limit)
		}
		if i > 0 {
			nd.recs = append(nd.recs, b[prev:o])
		}
		prev = o
	}
	return nd, nil
}

// key is the key of record i of node nd, whose key length field is first,
// and the bytes after it. In an index node, the key takes the tree's
// maximum key length unless the tree has variable-length index keys.
func (t *btree) key(nd *node, i int) (key, rest []byte, err error) {
	rec := nd.recs[i]
	if len(rec) < 2 {
		return nil, nil, corrupt("the %s B-tree's node %d has a record %d of %d bytes, too short for a key length", t.what, nd.num, i, len(rec))
	}
	kl := int(binary.BigEndian.Uint16(rec))
	size := 2 + kl
	if nd.kind == kindIndex && !t.varIndexKeys {
		if kl > t.maxKeyLen {
			return nil, nil, corrupt("the %s B-tree's node %d, record %d: its key length, %d, is past the tree's maximum, %d", t.what, nd.num, i, kl, t.maxKeyLen)
		}
		size = 2 + t.maxKeyLen
	}
	if size > len(rec) {
		return nil, nil, corrupt("the %s B-tree's node %d, record %d: its key length, %d, runs past its %d-byte record", t.what, nd.num, i, kl, len(rec))
	}
	if kl < t.minKeyLen {
		return nil, nil, corrupt("the %s B-tree's node %d, record %d: its key length, %d, is shorter than the %d its keys need", t.what, nd.num, i, kl, t.minKeyLen)
	}
	return rec[2 : 2+kl], rec[size:], nil
}

// walk visits the tree's leaf records in order, from the first that may
// come after every key before says is before the one sought. At each
// index node it follows the last record whose key is before, or the
// first if none is; at the leaves it follows forward links until visit
// says to stop. The descent is bounded by the tree's depth, and the leaf
// walk visits each node once at most, so a cycle is an error, never a
// hang.
func (t *btree) walk(before func(key []byte) (bool, error), visit func(key, rec []byte) (stop bool, err error)) error {
	if t.depth == 0 {
		return nil
	}
	// Each level's node must be at that level's height, which falls by
	// one at each step: so the descent reads at most depth nodes, and an
	// index that points back up is caught by its child's height.
	n := t.root
	var nd *node
	for h := t.depth; ; h-- {
		var err error
		if nd, err = t.readNode(n); err != nil {
			return err
		}
		if nd.height != h {
			return corrupt("the %s B-tree's node %d is at height %d, where its index puts it at %d", t.what, n, nd.height, h)
		}
		if h == 1 {
			if nd.kind != kindLeaf {
				return corrupt("the %s B-tree's node %d, at the leaves' height, is of kind %d, not a leaf", t.what, n, nd.kind)
			}
			break
		}
		if nd.kind != kindIndex {
			return corrupt("the %s B-tree's node %d, at height %d, is of kind %d, not an index node", t.what, n, h, nd.kind)
		}
		if len(nd.recs) == 0 {
			return corrupt("the %s B-tree's index node %d has no records", t.what, n)
		}
		for i := range nd.recs {
			key, rest, err := t.key(nd, i)
			if err != nil {
				return err
			}
			if len(rest) < 4 {
				return corrupt("the %s B-tree's index node %d, record %d, has no room for a child node after its key", t.what, n, i)
			}
			b, err := before(key)
			if err != nil {
				return err
			}
			if i == 0 || b {
				n = binary.BigEndian.Uint32(rest)
			}
			if !b {
				break
			}
		}
	}
	leaves := map[uint32]bool{nd.num: true}
	for {
		for i := range nd.recs {
			key, rec, err := t.key(nd, i)
			if err != nil {
				return err
			}
			stop, err := visit(key, rec)
			if err != nil || stop {
				return err
			}
		}
		next := nd.fLink
		if next == 0 {
			return nil
		}
		if leaves[next] {
			return corrupt("the %s B-tree's leaf nodes link back to node %d: a cycle", t.what, next)
		}
		leaves[next] = true
		var err error
		if nd, err = t.readNode(next); err != nil {
			return err
		}
		if nd.kind != kindLeaf || nd.height != 1 {
			return corrupt("the %s B-tree's node %d, linked as the next leaf, is of kind %d at height %d, not a leaf", t.what, next, nd.kind, nd.height)
		}
	}
}
