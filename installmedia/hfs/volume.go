// Package hfs reads an HFS+ or HFSX volume, read-only and in pure Go, so
// an install disc's files can be listed, hashed and copied on any host,
// with nothing mounted.
//
// It reads only what a disc's packages need: directories, and the data
// forks of regular files. It refuses, by name, what it does not follow:
// symbolic links, hard links, HFS+-compressed files and special files.
//
// A damaged or hostile volume gives a *CorruptError, never a panic, a hang
// or an allocation the volume's own size does not bound: every offset is
// checked against its node and the volume, every descent of a B-tree is
// bounded by its depth, and every walk of its leaves keeps the nodes it
// has seen.
//
// Lookups never compare names in a B-tree's order: a directory's children
// are found by their parent's CNID, a number, and a name is then matched
// by exact UTF-16 equality. So no case folding is needed, and an HFSX
// (case-sensitive) volume reads exactly as an HFS+ one does. Names are
// matched as the catalog holds them, which is case-sensitive even on HFS+
// and in decomposed form (NFD) for accented letters.
package hfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"strings"
	"unicode/utf16"
)

// Kind is what a catalog entry is.
type Kind int

const (
	Dir      Kind = iota + 1 // a folder
	File                     // a regular file
	Symlink                  // a symbolic link: type slnk, creator rhap
	HardLink                 // a hard link to a file (hlnk/hfs+) or a folder (fdrp/MACS)
	Other                    // a FIFO, a socket or a device
)

func (k Kind) String() string {
	switch k {
	case Dir:
		return "directory"
	case File:
		return "file"
	case Symlink:
		return "symbolic link"
	case HardLink:
		return "hard link"
	case Other:
		return "special file"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Entry is a catalog entry. Size is the data fork's length, which is 0
// for a directory and for an HFS+-compressed file, whose data is kept
// elsewhere.
type Entry struct {
	Name       string
	CNID       uint32
	Kind       Kind
	Size       int64
	Compressed bool
}

// CorruptError says a volume's own structures are inconsistent: an offset
// past its node or the volume, a cycle, an absurd count or a record too
// short for its type.
type CorruptError struct{ What string }

func (e *CorruptError) Error() string { return "damaged HFS+ volume: " + e.What }

func corrupt(format string, a ...any) error {
	return &CorruptError{What: fmt.Sprintf(format, a...)}
}

// Where the volume header and catalog records keep what this reader
// reads (Apple's Technical Note TN1150).
const (
	headerOffset    = 1024
	headerSize      = 512
	attrUnmounted   = 1 << 8
	attrJournaled   = 1 << 13
	rootParentCNID  = 1
	rootCNID        = 2
	extentsCNID     = 3
	catalogCNID     = 4
	extentsForkAt   = 192
	catalogForkAt   = 272
	fileDataForkAt  = 88
	ownerFlagsAt    = 41
	fileModeAt      = 42
	fileTypeAt      = 48
	ufCompressed    = 0x20
	folderRecordLen = 88
	fileRecordLen   = 248
)

// errDirtyJournal is a journaled volume that was not cleanly unmounted:
// its journal may hold changes that are not yet in its B-trees.
var errDirtyJournal = errors.New("the volume is journaled and was not cleanly unmounted, so its journal may hold changes this reader cannot replay: mount and unmount it on a Mac, or image it again from a clean disc")

// Volume is an open HFS+ or HFSX volume. Its methods are safe for
// concurrent use if its io.ReaderAt is.
type Volume struct {
	r         io.ReaderAt
	blockSize int64
	blocks    uint32
	hfsx      bool
	extents   *btree
	catalog   *btree
}

// Open reads the volume header of the size-byte volume in r, and the
// headers of its extents overflow and catalog B-trees.
func Open(r io.ReaderAt, size int64) (*Volume, error) {
	if size < headerOffset+headerSize {
		return nil, corrupt("the volume is %d bytes, too small to hold a volume header", size)
	}
	h := make([]byte, headerSize)
	if n, err := r.ReadAt(h, headerOffset); n < len(h) {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF // the volume is shorter than size says
		}
		return nil, fmt.Errorf("reading the volume header: %w", err)
	}
	v := &Volume{r: r}
	switch sig, ver := string(h[0:2]), binary.BigEndian.Uint16(h[2:]); {
	case sig == "H+" && ver == 4:
	case sig == "HX" && ver == 5:
		v.hfsx = true
	default:
		return nil, corrupt("the volume header's signature is %q, version %d, which is neither HFS+ (H+, 4) nor HFSX (HX, 5)", sig, ver)
	}
	if a := binary.BigEndian.Uint32(h[4:]); a&attrJournaled != 0 && a&attrUnmounted == 0 {
		return nil, errDirtyJournal
	}
	bs := binary.BigEndian.Uint32(h[40:])
	if bs < 512 || bs&(bs-1) != 0 {
		return nil, corrupt("the volume header's block size, %d, is not a power of two of at least 512", bs)
	}
	v.blockSize = int64(bs)
	v.blocks = binary.BigEndian.Uint32(h[44:])
	if uint64(v.blocks)*uint64(bs) > uint64(size) {
		return nil, corrupt("the volume header gives %d blocks of %d bytes, past the end of the volume's %d bytes", v.blocks, bs, size)
	}

	ef, err := v.newFork(extentsCNID, h[extentsForkAt:], "the extents overflow file")
	if err != nil {
		return nil, err
	}
	if v.extents, err = openBTree(ef, "extents overflow", 10); err != nil {
		return nil, err
	}
	cf, err := v.newFork(catalogCNID, h[catalogForkAt:], "the catalog file")
	if err != nil {
		return nil, err
	}
	if v.catalog, err = openBTree(cf, "catalog", 6); err != nil {
		return nil, err
	}
	return v, nil
}

// Stat is the entry at path, a slash-separated path from the volume's
// root, which is "".
func (v *Volume) Stat(path string) (Entry, error) {
	e, _, err := v.lookup(path)
	return e, err
}

// ReadDir lists the directory at path in the catalog's order.
func (v *Volume) ReadDir(path string) ([]Entry, error) {
	e, _, err := v.lookup(path)
	if err != nil {
		return nil, err
	}
	if e.Kind != Dir {
		return nil, notADirectory(path, e.Kind)
	}
	kids, err := v.children(e.CNID)
	if err != nil {
		return nil, err
	}
	es := make([]Entry, 0, len(kids))
	for _, k := range kids {
		es = append(es, k.entry)
	}
	return es, nil
}

// Open is a reader of the data fork of the regular file at path. It
// refuses a directory, a link, a special file and a compressed file,
// whose data fork does not hold its bytes.
func (v *Volume) Open(path string) (io.Reader, Entry, error) {
	e, rec, err := v.lookup(path)
	if err != nil {
		return nil, Entry{}, err
	}
	switch {
	case e.Kind == Dir:
		return nil, e, fmt.Errorf("%s is a directory, not a file", path)
	case e.Kind == Symlink:
		return nil, e, fmt.Errorf("%s is a symbolic link, which this reader does not follow", path)
	case e.Kind == HardLink:
		return nil, e, fmt.Errorf("%s is a hard link, whose data is in an indirect node this reader does not follow", path)
	case e.Kind != File:
		return nil, e, fmt.Errorf("%s is a %s, not a regular file", path, e.Kind)
	case e.Compressed:
		return nil, e, fmt.Errorf("%s is compressed (HFS+ compression keeps a file's data in an extended attribute or its resource fork), which this reader does not decompress", path)
	}
	f, err := v.dataFork(e.CNID, rec)
	if err != nil {
		return nil, e, fmt.Errorf("%s: %w", path, err)
	}
	return io.NewSectionReader(f, 0, f.size), e, nil
}

// lookup is the entry at path and its catalog record's body.
func (v *Volume) lookup(path string) (Entry, []byte, error) {
	cur, err := v.root()
	if err != nil {
		return Entry{}, nil, err
	}
	trimmed := strings.TrimSuffix(path, "/")
	if trimmed == "" {
		return cur.entry, cur.rec, nil
	}
	parts := strings.Split(trimmed, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return Entry{}, nil, fmt.Errorf("%q is not a plain path from the volume's root: it has an empty, . or .. name", path)
		}
	}
	for i, p := range parts {
		if cur.entry.Kind != Dir {
			return Entry{}, nil, notADirectory(strings.Join(parts[:i], "/"), cur.entry.Kind)
		}
		kids, err := v.children(cur.entry.CNID)
		if err != nil {
			return Entry{}, nil, err
		}
		want := utf16.Encode([]rune(p))
		found := false
		for _, k := range kids {
			if equalUTF16(k.name, want) {
				cur, found = k, true
				break
			}
		}
		if !found {
			return Entry{}, nil, fmt.Errorf("%s: %w", trimmed, fs.ErrNotExist)
		}
	}
	return cur.entry, cur.rec, nil
}

func notADirectory(path string, k Kind) error {
	switch k {
	case Symlink:
		return fmt.Errorf("%s is a symbolic link, which this reader does not follow", path)
	case HardLink:
		return fmt.Errorf("%s is a hard link, which this reader does not follow", path)
	}
	return fmt.Errorf("%s is not a directory: it is a %s", path, k)
}

func equalUTF16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dataFork is the data fork of the file record rec, whose CNID is cnid,
// with every extent found, overflow ones included.
func (v *Volume) dataFork(cnid uint32, rec []byte) (*fork, error) {
	return v.newFork(cnid, rec[fileDataForkAt:], fmt.Sprintf("the data fork of CNID %d", cnid))
}

// checkForkSize is the logical size of the HFSPlusForkData at fd, if its
// blocks hold it and the volume holds its blocks.
func (v *Volume) checkForkSize(fd []byte, what string) (int64, error) {
	logical := binary.BigEndian.Uint64(fd)
	blocks := binary.BigEndian.Uint32(fd[12:])
	if blocks > v.blocks {
		return 0, corrupt("%s claims %d blocks, past the end of the volume's %d", what, blocks, v.blocks)
	}
	if logical > uint64(blocks)*uint64(v.blockSize) || logical > math.MaxInt64 {
		return 0, corrupt("%s has a logical size of %d bytes, past its %d blocks of %d", what, logical, blocks, v.blockSize)
	}
	return int64(logical), nil
}
