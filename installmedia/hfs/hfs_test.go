package hfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Mavergreen/longboat/installmedia/internal/fixture"
)

// noise is n bytes that never repeat a block, so a block read from the
// wrong place shows.
func noise(seed uint64, n int) []byte {
	rng := rand.New(rand.NewPCG(seed, 7))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

func open(t *testing.T, img []byte) *Volume {
	t.Helper()
	v, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return v
}

func readFile(t *testing.T, v *Volume, path string) []byte {
	t.Helper()
	r, _, err := v.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	return b
}

func names(es []Entry) []string {
	var n []string
	for _, e := range es {
		n = append(n, e.Name)
	}
	return n
}

func treeFiles() []fixture.File {
	return []fixture.File{
		{Path: "Packages/OSInstall.mpkg", Data: []byte("an mpkg")},
		{Path: "Packages/BSD.pkg", Data: noise(1, 10000)},
		{Path: "Packages/.DS_Store", Data: []byte{0, 0, 0, 1}},
		{Path: "System/Library/CoreServices/SystemVersion.plist", Data: []byte("<plist>10.6.3</plist>\n")},
		{Path: "System/Empty.txt", Data: nil},
		{Path: "Readme.txt", Data: []byte("read me")},
		{Path: "Empty/"},
	}
}

var layouts = map[string]fixture.HFSOptions{
	"default":                   {},
	"512-byte blocks":           {BlockSize: 512, NodeSize: 512},
	"8 KiB nodes, 2 KiB blocks": {BlockSize: 2048, NodeSize: 8192},
	"HFSX":                      {HFSX: true},
	"journaled":                 {Journaled: true},
}

func TestReadsFilesAndDirectories(t *testing.T) {
	for name, o := range layouts {
		t.Run(name, func(t *testing.T) {
			files := treeFiles()
			v := open(t, fixture.HFS(files, o))

			root, err := v.Stat("")
			if err != nil || root.Kind != Dir || root.CNID != 2 {
				t.Fatalf(`Stat("") = %+v, %v; want the root directory, CNID 2`, root, err)
			}
			es, err := v.ReadDir("")
			if err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Join(names(es), ","), "Empty,Packages,Readme.txt,System"; got != want {
				t.Fatalf(`ReadDir("") = %s; want %s`, got, want)
			}
			kinds := map[string]Kind{"Empty": Dir, "Packages": Dir, "Readme.txt": File, "System": Dir}
			for _, e := range es {
				if e.Kind != kinds[e.Name] || e.CNID < 16 {
					t.Errorf("%s: kind %v, CNID %d; want %v and a user CNID", e.Name, e.Kind, e.CNID, kinds[e.Name])
				}
			}
			pk, err := v.ReadDir("Packages")
			if err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Join(names(pk), ","), ".DS_Store,BSD.pkg,OSInstall.mpkg"; got != want {
				t.Fatalf(`ReadDir("Packages") = %s; want %s`, got, want)
			}
			if es, err := v.ReadDir("Empty"); err != nil || len(es) != 0 {
				t.Fatalf(`ReadDir("Empty") = %v, %v; want no entries`, es, err)
			}
			if es, err := v.ReadDir("Packages/"); err != nil || len(es) != 3 {
				t.Fatalf(`ReadDir("Packages/") = %v, %v; want the three packages`, es, err)
			}
			for _, f := range files {
				if strings.HasSuffix(f.Path, "/") {
					continue
				}
				e, err := v.Stat(f.Path)
				if err != nil {
					t.Fatalf("Stat(%q): %v", f.Path, err)
				}
				if e.Kind != File || e.Size != int64(len(f.Data)) || e.Compressed || e.Name != f.Path[strings.LastIndex(f.Path, "/")+1:] {
					t.Errorf("Stat(%q) = %+v; want a %d-byte uncompressed file", f.Path, e, len(f.Data))
				}
				if got := readFile(t, v, f.Path); !bytes.Equal(got, f.Data) {
					t.Errorf("%s: read %d bytes that are not its %d", f.Path, len(got), len(f.Data))
				}
				if _, oe, _ := v.Open(f.Path); oe != e {
					t.Errorf("Open(%q) gave %+v; Stat gave %+v", f.Path, oe, e)
				}
			}
			if e, err := v.Stat("System/Library"); err != nil || e.Kind != Dir || e.Name != "Library" {
				t.Fatalf(`Stat("System/Library") = %+v, %v`, e, err)
			}
			if _, err := v.Stat("Packages/Nope.pkg"); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "Nope.pkg") {
				t.Fatalf("a missing file: err = %v; want fs.ErrNotExist naming it", err)
			}
			if _, err := v.ReadDir("Readme.txt"); err == nil || !strings.Contains(err.Error(), "not a directory") {
				t.Fatalf("ReadDir of a file: err = %v; want one saying it is not a directory", err)
			}
			if _, err := v.Stat("Readme.txt/x"); err == nil || !strings.Contains(err.Error(), "not a directory") {
				t.Fatalf("Stat through a file: err = %v; want one saying it is not a directory", err)
			}
			if _, _, err := v.Open("System"); err == nil || !strings.Contains(err.Error(), "directory") {
				t.Fatalf("Open of a directory: err = %v; want one naming a directory", err)
			}
			for _, bad := range []string{"/Packages", "Packages//BSD.pkg", "./Packages", "Packages/../System"} {
				if _, err := v.Stat(bad); err == nil {
					t.Errorf("Stat(%q) succeeded; want a path refused as not plain", bad)
				}
			}
		})
	}
}

func TestAVolumeReadsConcurrently(t *testing.T) {
	files := treeFiles()
	v := open(t, fixture.HFS(files, fixture.HFSOptions{BlockSize: 512, NodeSize: 512, Fragment: []string{"Packages/BSD.pkg"}}))
	var wg sync.WaitGroup
	errs := make(chan error, 8*len(files))
	for range 8 {
		for _, f := range files {
			wg.Go(func() {
				if strings.HasSuffix(f.Path, "/") {
					if _, err := v.ReadDir(f.Path); err != nil {
						errs <- err
					}
					return
				}
				r, _, err := v.Open(f.Path)
				if err != nil {
					errs <- err
					return
				}
				if b, err := io.ReadAll(r); err != nil || !bytes.Equal(b, f.Data) {
					errs <- fmt.Errorf("%s: read %d bytes, not its %d (%v)", f.Path, len(b), len(f.Data), err)
				}
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestNamesMatchExactly(t *testing.T) {
	v := open(t, fixture.HFS(treeFiles(), fixture.HFSOptions{}))
	// HFS+ is case-insensitive on a Mac; this reader never folds case,
	// so a name must be given as the catalog holds it.
	if _, err := v.Stat("packages"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(`Stat("packages") = %v; want fs.ErrNotExist: names match exactly`, err)
	}
}

func TestHFSXNamesAreCaseSensitive(t *testing.T) {
	files := []fixture.File{
		{Path: "Packages/README", Data: []byte("upper")},
		{Path: "Packages/Readme", Data: []byte("mixed")},
		{Path: "Packages/readme", Data: []byte("lower")},
		{Path: "packages/x", Data: []byte("the other directory")},
	}
	v := open(t, fixture.HFS(files, fixture.HFSOptions{HFSX: true}))
	if !v.hfsx {
		t.Fatal("an HX volume was not read as HFSX")
	}
	es, err := v.ReadDir("Packages")
	if err != nil {
		t.Fatal(err)
	}
	// HFSX orders names by their UTF-16 code units: upper case first.
	if got, want := strings.Join(names(es), ","), "README,Readme,readme"; got != want {
		t.Fatalf(`ReadDir("Packages") = %s; want %s`, got, want)
	}
	for _, f := range files {
		if got := readFile(t, v, f.Path); !bytes.Equal(got, f.Data) {
			t.Errorf("%s = %q; want %q", f.Path, got, f.Data)
		}
	}
	if _, err := v.Stat("PACKAGES/x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(`Stat("PACKAGES/x") = %v; want fs.ErrNotExist`, err)
	}
}

func TestDeepCatalogsAreWalked(t *testing.T) {
	var files []fixture.File
	var want []string
	for i := range 400 {
		name := fmt.Sprintf("file%03d.pkg", i)
		want = append(want, name)
		files = append(files, fixture.File{Path: "Packages/" + name, Data: []byte(name)})
	}
	// Neighbours on both sides, so the walk must start and stop between
	// other directories' records.
	files = append(files,
		fixture.File{Path: "Aardvark/a", Data: []byte("a")},
		fixture.File{Path: "Packages/Sub/inner", Data: []byte("inner")},
		fixture.File{Path: "Zebra/z", Data: []byte("z")},
	)
	want = append(want, "Sub")
	v := open(t, fixture.HFS(files, fixture.HFSOptions{NodeSize: 512}))
	if v.catalog.depth < 3 {
		t.Fatalf("the catalog's B-tree is %d deep; this test needs at least 3", v.catalog.depth)
	}
	t.Logf("the catalog's B-tree is %d deep", v.catalog.depth)
	es, err := v.ReadDir("Packages")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(es); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ReadDir listed %d entries; want the %d, each once, in order:\n%v", len(got), len(want), got)
	}
	for p, want := range map[string]string{
		"Packages/file000.pkg": "file000.pkg",
		"Packages/file199.pkg": "file199.pkg",
		"Packages/file399.pkg": "file399.pkg",
		"Packages/Sub/inner":   "inner",
		"Aardvark/a":           "a",
		"Zebra/z":              "z",
	} {
		if got := string(readFile(t, v, p)); got != want {
			t.Errorf("%s = %q; want %q", p, got, want)
		}
	}
}

func TestFragmentedForksUseOverflowExtents(t *testing.T) {
	big := noise(2, 20*512+100) // 21 blocks: 8 in its record, 13 in two overflow records
	other := noise(3, 11*512)   // 11 blocks, interleaved with big's
	var files []fixture.File
	for i := range 60 {
		files = append(files, fixture.File{Path: fmt.Sprintf("Fill/f%02d", i), Data: []byte{byte(i)}})
	}
	files = append(files,
		fixture.File{Path: "Packages/Big.pkg", Data: big},
		fixture.File{Path: "Packages/Other.pkg", Data: other},
		fixture.File{Path: "Packages/Plain.pkg", Data: noise(4, 3000)},
	)
	img := fixture.HFS(files, fixture.HFSOptions{
		BlockSize: 512, NodeSize: 512,
		Fragment: []string{"Packages/Big.pkg", "Packages/Other.pkg", fixture.CatalogFile},
	})
	v := open(t, img)
	if n := len(v.catalog.f.extents); n <= 8 {
		t.Fatalf("the catalog has %d extents; this test needs it to use overflow extents", n)
	}
	for _, f := range files {
		if got := readFile(t, v, f.Path); !bytes.Equal(got, f.Data) {
			t.Fatalf("%s: read %d bytes that are not its %d", f.Path, len(got), len(f.Data))
		}
	}
	e, err := v.Stat("Packages/Big.pkg")
	if err != nil {
		t.Fatal(err)
	}
	fk, err := v.dataFork(e.CNID, mustRecord(t, v, "Packages/Big.pkg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fk.extents) != 21 {
		t.Fatalf("Big.pkg has %d extents; want 21, one a block", len(fk.extents))
	}
}

// resourceForkVolume holds two fragmented files, P/A.pkg and P/B.pkg,
// each with two overflow extent records, and moves A's fork to its
// resource fork: its file record's data fork becomes empty, and its
// overflow records' fork type becomes 0xFF. Extent keys sort by file ID
// first (TN1150), so A's resource-fork records, of the lower CNID, sit
// before B's data-fork records.
func resourceForkVolume(t *testing.T) (img []byte, a, b []byte) {
	t.Helper()
	a, b = noise(5, 20*512), noise(6, 20*512)
	img = fixture.HFS([]fixture.File{{Path: "P/A.pkg", Data: a}, {Path: "P/B.pkg", Data: b}},
		fixture.HFSOptions{BlockSize: 512, NodeSize: 512, Fragment: []string{"P/A.pkg", "P/B.pkg"}})
	l := parseLayout(t, img)
	body := l.body(t, "A.pkg")
	cnid := binary.BigEndian.Uint32(body[8:])
	copy(body[168:248], body[88:168])
	clear(body[88:168])
	moved := 0
	for n := binary.BigEndian.Uint32(l.extNodeAt(0)[14+10:]); n != 0; {
		node := l.extNodeAt(int(n))
		for i := range int(binary.BigEndian.Uint16(node[10:])) {
			off := int(binary.BigEndian.Uint16(node[len(node)-2*(i+1):]))
			if binary.BigEndian.Uint32(node[off+4:]) == cnid {
				node[off+2] = 0xff // the resource fork
				moved++
			}
		}
		n = binary.BigEndian.Uint32(node)
	}
	if moved != 2 {
		t.Fatalf("moved %d of A.pkg's overflow records to its resource fork; want 2", moved)
	}
	return img, a, b
}

func TestAResourceForksOverflowRecordsDoNotHideTheNextFiles(t *testing.T) {
	img, _, b := resourceForkVolume(t)
	v := open(t, img)
	if got := readFile(t, v, "P/B.pkg"); !bytes.Equal(got, b) {
		t.Fatalf("P/B.pkg: read %d bytes that are not its %d", len(got), len(b))
	}
	if e, err := v.Stat("P/A.pkg"); err != nil || e.Size != 0 {
		t.Fatalf(`Stat("P/A.pkg") = %+v, %v; want an empty data fork`, e, err)
	}
	if got := readFile(t, v, "P/A.pkg"); len(got) != 0 {
		t.Fatalf("P/A.pkg's data fork read %d bytes; want none", len(got))
	}
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.ReaderAt
	n atomic.Int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}

// TestOverflowExtentsAreFoundInOneWalk holds that a fork's overflow
// records are read in one walk of the extents B-tree, so a fork of 5,000
// overflow records costs one pass over the tree, not one per record --
// even when the tree sends every descent to its first leaf, which made a
// lookup per record quadratic: hours, for a few hundred thousand.
func TestOverflowExtentsAreFoundInOneWalk(t *testing.T) {
	const records = 5000
	data := make([]byte, (8+8*records)*512)
	for i := 0; i < len(data); i += 512 {
		binary.BigEndian.PutUint64(data[i:], uint64(i/512)+1)
	}
	built := fixture.HFS([]fixture.File{{Path: "P/Big.pkg", Data: data}},
		fixture.HFSOptions{BlockSize: 512, NodeSize: 512, Fragment: []string{"P/Big.pkg"}})
	for name, damage := range map[string]func(l layout){
		"as built": func(layout) {},
		"every descent sent to the first leaf": func(l layout) {
			h := l.extNodeAt(0)[14:]
			binary.BigEndian.PutUint16(h[0:], 1)
			copy(h[2:6], h[10:14]) // the root is the first leaf
		},
	} {
		t.Run(name, func(t *testing.T) {
			img := append([]byte(nil), built...)
			l := parseLayout(t, img)
			damage(l)
			h := img[1024:]
			trees := int64(binary.BigEndian.Uint64(h[192:]) + binary.BigEndian.Uint64(h[272:]))
			cr := &countingReader{r: bytes.NewReader(img)}
			v, err := Open(cr, int64(len(img)))
			if err != nil {
				t.Fatal(err)
			}
			cr.n.Store(0)
			r, _, err := v.Open("P/Big.pkg")
			if err != nil {
				t.Fatal(err)
			}
			if read := cr.n.Load(); read > trees {
				t.Fatalf("finding Big.pkg's %d overflow records read %d bytes, more than the catalog and extents files' %d together", records, read, trees)
			}
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("Big.pkg read %d bytes (%v) that are not its %d", len(got), err, len(data))
			}
		})
	}
}

// shortReader gives at most n bytes from b, with err.
type shortReader struct {
	b   []byte
	n   int
	err error
}

func (s shortReader) ReadAt(p []byte, off int64) (int, error) {
	return copy(p[:min(len(p), s.n)], s.b[off:]), s.err
}

func TestAShortHeaderReadIsAnUnexpectedEOF(t *testing.T) {
	img := fixture.HFS(treeFiles(), fixture.HFSOptions{})
	for _, e := range []error{nil, io.EOF} {
		_, err := Open(shortReader{img, 100, e}, int64(len(img)))
		if !errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "%!") {
			t.Errorf("a short read giving %v: err = %v; want io.ErrUnexpectedEOF", e, err)
		}
	}
}

func mustRecord(t *testing.T, v *Volume, path string) []byte {
	t.Helper()
	_, rec, err := v.lookup(path)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestFailClosedKinds(t *testing.T) {
	files := []fixture.File{
		{Path: "Packages/Compressed.pkg", Data: []byte("held elsewhere"), Compressed: true},
		{Path: "Packages/Linked.pkg", HardLink: true},
		{Path: "Packages/Pointer.pkg", Symlink: "Compressed.pkg"},
		{Path: "Real/x.pkg", Data: []byte("x")},
		{Path: "Linked", Symlink: "Real"},
	}
	v := open(t, fixture.HFS(files, fixture.HFSOptions{}))
	for _, c := range []struct {
		path, says string
		kind       Kind
		compressed bool
	}{
		{"Packages/Compressed.pkg", "compressed", File, true},
		{"Packages/Linked.pkg", "hard link", HardLink, false},
		{"Packages/Pointer.pkg", "symbolic link", Symlink, false},
		{"Linked", "symbolic link", Symlink, false},
	} {
		e, err := v.Stat(c.path)
		if err != nil || e.Kind != c.kind || e.Compressed != c.compressed {
			t.Errorf("Stat(%q) = %+v, %v; want kind %v, compressed %v", c.path, e, err, c.kind, c.compressed)
		}
		if _, _, err := v.Open(c.path); err == nil || !strings.Contains(err.Error(), c.says) || !strings.Contains(err.Error(), c.path) {
			t.Errorf("Open(%q): err = %v; want one naming it and saying %q", c.path, err, c.says)
		}
	}
	if _, err := v.Stat("Linked/x.pkg"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Stat through a symlinked directory: err = %v; want one naming the symbolic link", err)
	}
	if _, err := v.ReadDir("Linked"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("ReadDir of a symlinked directory: err = %v; want one naming the symbolic link", err)
	}
	for k, s := range map[Kind]string{Dir: "directory", File: "file", Symlink: "symbolic link", HardLink: "hard link", Other: "special file"} {
		if k.String() != s {
			t.Errorf("Kind %d says %q; want %q", int(k), k.String(), s)
		}
	}
}

func TestADirtyJournalIsRefused(t *testing.T) {
	files := treeFiles()
	for _, c := range []struct {
		o      fixture.HFSOptions
		refuse bool
	}{
		{fixture.HFSOptions{Journaled: true, Dirty: true}, true},
		{fixture.HFSOptions{Journaled: true}, false},
		{fixture.HFSOptions{Dirty: true}, false},
	} {
		img := fixture.HFS(files, c.o)
		_, err := Open(bytes.NewReader(img), int64(len(img)))
		if c.refuse && (err == nil || !strings.Contains(err.Error(), "not cleanly unmounted")) {
			t.Errorf("%+v: err = %v; want one saying the volume was not cleanly unmounted", c.o, err)
		}
		if !c.refuse && err != nil {
			t.Errorf("%+v: %v; want it opened", c.o, err)
		}
	}
}

// layout is where a fixture keeps what the corruption cases damage. The
// fixture's catalog and extents files are contiguous unless fragmented.
type layout struct {
	img              []byte
	blockSize        int
	catalog, extents int // byte offsets of the B-trees
	catNode, extNode int // node sizes
	totalBlocks      uint32
}

func parseLayout(t *testing.T, img []byte) layout {
	t.Helper()
	h := img[1024:]
	l := layout{img: img, blockSize: int(binary.BigEndian.Uint32(h[40:]))}
	l.totalBlocks = binary.BigEndian.Uint32(h[44:])
	l.extents = int(binary.BigEndian.Uint32(h[192+16:])) * l.blockSize
	l.catalog = int(binary.BigEndian.Uint32(h[272+16:])) * l.blockSize
	l.catNode = int(binary.BigEndian.Uint16(img[l.catalog+14+18:]))
	l.extNode = int(binary.BigEndian.Uint16(img[l.extents+14+18:]))
	return l
}

func (l layout) catNodeAt(n int) []byte { return l.img[l.catalog+n*l.catNode:][:l.catNode] }
func (l layout) extNodeAt(n int) []byte { return l.img[l.extents+n*l.extNode:][:l.extNode] }

// catHeader is the catalog's header record.
func (l layout) catHeader() []byte { return l.catNodeAt(0)[14:] }

// firstLeaf is the catalog's first leaf node.
func (l layout) firstLeaf() []byte {
	return l.catNodeAt(int(binary.BigEndian.Uint32(l.catHeader()[10:])))
}

// record is the catalog leaf record, from its key, whose key names name
// and whose body is a file (2) or folder (1) record.
func (l layout) record(t *testing.T, name string) []byte {
	t.Helper()
	var key []byte
	key = binary.BigEndian.AppendUint16(key, uint16(len(name)))
	for _, c := range utf16.Encode([]rune(name)) {
		key = binary.BigEndian.AppendUint16(key, c)
	}
	cat := l.img[l.catalog:]
	for at := 0; ; {
		i := bytes.Index(cat[at:], key)
		if i < 0 {
			t.Fatalf("no catalog key names %q", name)
		}
		r := cat[at+i-6:]
		klen := int(binary.BigEndian.Uint16(r))
		if typ := binary.BigEndian.Uint16(r[2+klen:]); klen == 6+2*len(name) && (typ == 1 || typ == 2) {
			return r
		}
		at += i + 1
	}
}

// body is the body of the record record gives.
func (l layout) body(t *testing.T, name string) []byte {
	t.Helper()
	r := l.record(t, name)
	return r[2+int(binary.BigEndian.Uint16(r)):]
}

func TestCorruptVolumesFailClosed(t *testing.T) {
	var files []fixture.File
	for i := range 60 {
		files = append(files, fixture.File{Path: fmt.Sprintf("Packages/p%02d.pkg", i), Data: noise(uint64(i), 700)})
	}
	files = append(files, fixture.File{Path: "Packages/Frag.pkg", Data: noise(99, 12*512)})
	o := fixture.HFSOptions{BlockSize: 512, NodeSize: 512, Fragment: []string{"Packages/Frag.pkg"}}
	for _, c := range []struct {
		name, says string
		damage     func(t *testing.T, l layout)
	}{
		{"bad signature", "is neither HFS+ (H+, 4) nor HFSX (HX, 5)", func(t *testing.T, l layout) { copy(l.img[1024:], "XY") }},
		{"block size not a power of two", "block size, 3000, is not a power of two of at least 512", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint32(l.img[1024+40:], 3000)
		}},
		{"volume shorter than its header says", "the volume header gives 896 blocks of 512 bytes, past the end of the volume's", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint32(l.img[1024+44:], l.totalBlocks*4)
		}},
		{"catalog extent past volume", "the catalog file has an extent of 72 blocks at block 223, past the end of the volume's 224 blocks", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint32(l.img[1024+272+16:], l.totalBlocks-1)
		}},
		{"absurd node size", "the catalog B-tree's node size, 100, is not a power of two", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint16(l.catHeader()[18:], 100)
		}},
		{"root node out of range", "the catalog B-tree's root node, 4294967280, is not among its nodes", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint32(l.catHeader()[2:], 0xfffffff0)
		}},
		{"node offset past node", "has a record offset of 552 for record 0", func(t *testing.T, l layout) {
			n := l.firstLeaf()
			binary.BigEndian.PutUint16(n[len(n)-2:], uint16(len(n)+40))
		}},
		{"record offsets out of order", "has a record offset of 14 for record 1", func(t *testing.T, l layout) {
			n := l.firstLeaf()
			binary.BigEndian.PutUint16(n[len(n)-4:], 14)
			binary.BigEndian.PutUint16(n[len(n)-2:], 100)
		}},
		{"record count past node capacity", "claims 65535 records, more than its 512 bytes hold", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint16(l.firstLeaf()[10:], 0xffff)
		}},
		{"fLink cycle", "the catalog B-tree's leaf nodes link back to node", func(t *testing.T, l layout) {
			first := binary.BigEndian.Uint32(l.catHeader()[10:])
			second := binary.BigEndian.Uint32(l.catNodeAt(int(first))[0:])
			third := binary.BigEndian.Uint32(l.catNodeAt(int(second))[0:])
			binary.BigEndian.PutUint32(l.catNodeAt(int(third))[0:], first)
		}},
		{"fLink to an index node", "linked as the next leaf, is of kind 0", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint32(l.firstLeaf()[0:], binary.BigEndian.Uint32(l.catHeader()[2:]))
		}},
		{"index pointing back up to its own node", "where its index puts it at", func(t *testing.T, l layout) {
			root := l.catNodeAt(int(binary.BigEndian.Uint32(l.catHeader()[2:])))
			off := int(binary.BigEndian.Uint16(root[len(root)-2:]))
			klen := int(binary.BigEndian.Uint16(root[off:]))
			binary.BigEndian.PutUint32(root[off+2+klen:], binary.BigEndian.Uint32(l.catHeader()[2:]))
		}},
		{"tree depth past any real catalog", "the catalog B-tree's depth, 200, is past the 16 levels", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint16(l.catHeader()[0:], 200)
		}},
		{"extent past volume", "Packages/p07.pkg: damaged HFS+ volume: the data fork of CNID 25 has an extent of 2 blocks at block 223, past the end", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint32(l.body(t, "p07.pkg")[88+16:], l.totalBlocks-1)
		}},
		{"logical size past its blocks", "the data fork of \"p08.pkg\" has a logical size of 1099511627776 bytes", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint64(l.body(t, "p08.pkg")[88:], 1<<40)
		}},
		{"catalog key length past record", "its key length, 32752, runs past its", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint16(l.record(t, "p09.pkg"), 0x7ff0)
		}},
		{"catalog record shorter than its type", "the catalog's file record for \"Packages\" is 88 bytes, shorter than 248", func(t *testing.T, l layout) {
			binary.BigEndian.PutUint16(l.body(t, "Packages"), 2) // a file record's type, on a folder record's 88 bytes
		}},
		{"overflow extents missing", "needs an overflow extent record for its blocks from 8, and the extents overflow file has none", func(t *testing.T, l layout) {
			h := l.extNodeAt(0)[14:]
			binary.BigEndian.PutUint16(h[0:], 0)  // depth
			binary.BigEndian.PutUint32(h[2:], 0)  // root node
			binary.BigEndian.PutUint32(h[6:], 0)  // leaf records
			binary.BigEndian.PutUint32(h[10:], 0) // first leaf
			binary.BigEndian.PutUint32(h[14:], 0) // last leaf
		}},
		{"overflow extent that adds no blocks", "has an overflow extent record, for its blocks from 8, that adds no blocks", func(t *testing.T, l layout) {
			leaf := l.extNodeAt(int(binary.BigEndian.Uint32(l.extNodeAt(0)[14+10:])))
			off := int(binary.BigEndian.Uint16(leaf[len(leaf)-2:]))
			clear(leaf[off+12 : off+12+64])
		}},
		{"overflow record at the wrong block", "has an overflow extent record starting at block 9 of the fork, where its extents so far end at block 8", func(t *testing.T, l layout) {
			leaf := l.extNodeAt(int(binary.BigEndian.Uint32(l.extNodeAt(0)[14+10:])))
			off := int(binary.BigEndian.Uint16(leaf[len(leaf)-2:]))
			binary.BigEndian.PutUint32(leaf[off+8:], 9) // the key's start block
		}},
		{"truncated image", "the volume header gives 224 blocks of 512 bytes, past the end of the volume's 57344 bytes", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			img := fixture.HFS(files, o)
			if c.damage != nil {
				c.damage(t, parseLayout(t, img))
			} else {
				img = img[:len(img)/2]
			}
			err := withinASecond(t, func() error { return walk(img) })
			var ce *CorruptError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v (%T); want a *CorruptError", err, err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Fatalf("err = %v; want one saying %q", err, c.says)
			}
			t.Log(err)
		})
	}
	t.Run("undamaged", func(t *testing.T) {
		if err := walk(fixture.HFS(files, o)); err != nil {
			t.Fatalf("the undamaged volume fails: %v", err)
		}
	})
}

// walk opens img and reads all of it: every directory, and every regular
// file's bytes.
func walk(img []byte) error {
	v, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		return err
	}
	// A damaged catalog can give a folder its own ancestor's CNID, so
	// each directory is walked once.
	seen := map[uint32]bool{}
	var dir func(string) error
	dir = func(p string) error {
		es, err := v.ReadDir(p)
		if err != nil {
			return err
		}
		for _, e := range es {
			child := e.Name
			if p != "" {
				child = p + "/" + e.Name
			}
			if _, err := v.Stat(child); err != nil {
				return err
			}
			switch {
			case e.Kind == Dir && !seen[e.CNID]:
				seen[e.CNID] = true
				if err := dir(child); err != nil {
					return err
				}
			case e.Kind == File && !e.Compressed:
				r, _, err := v.Open(child)
				if err != nil {
					return err
				}
				if _, err := io.Copy(io.Discard, r); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return dir("")
}

// withinASecond runs f, failing the test if it panics or takes more than a
// second.
func withinASecond(t *testing.T, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- f()
	}()
	select {
	case err := <-done:
		if err != nil && strings.HasPrefix(err.Error(), "panic: ") {
			t.Fatal(err)
		}
		return err
	case <-time.After(time.Second):
		t.Fatal("still running after a second: a hang")
	}
	return nil
}

func TestCorruptErrorsSayWhatIsDamaged(t *testing.T) {
	err := error(&CorruptError{What: "the catalog's root node is 7, past its 4 nodes"})
	if got, want := err.Error(), "damaged HFS+ volume: the catalog's root node is 7, past its 4 nodes"; got != want {
		t.Fatalf("Error() = %q; want %q", got, want)
	}
}

// FuzzOpen holds that no volume, however damaged, panics or hangs the
// reader. Its seeds, every fixture above, run as a test.
func FuzzOpen(f *testing.F) {
	for _, o := range layouts {
		f.Add(fixture.HFS(treeFiles(), o))
	}
	f.Add(fixture.HFS(treeFiles(), fixture.HFSOptions{Journaled: true, Dirty: true}))
	f.Add(fixture.HFS([]fixture.File{
		{Path: "Packages/Compressed.pkg", Data: []byte("c"), Compressed: true},
		{Path: "Packages/Linked.pkg", HardLink: true},
		{Path: "Packages/Pointer.pkg", Symlink: "Compressed.pkg"},
		{Path: "README", Data: []byte("u")},
	}, fixture.HFSOptions{HFSX: true}))
	var many []fixture.File
	for i := range 40 {
		many = append(many, fixture.File{Path: fmt.Sprintf("Packages/p%02d.pkg", i), Data: noise(uint64(i), 600)})
	}
	f.Add(fixture.HFS(many, fixture.HFSOptions{BlockSize: 512, NodeSize: 512, Fragment: []string{"Packages/p01.pkg", fixture.CatalogFile}}))
	f.Fuzz(func(t *testing.T, img []byte) {
		v, err := Open(bytes.NewReader(img), int64(len(img)))
		if err != nil {
			return
		}
		es, err := v.ReadDir("")
		if err != nil {
			return
		}
		for _, e := range es {
			if e.Kind == Dir {
				v.ReadDir(e.Name)
				continue
			}
			if r, _, err := v.Open(e.Name); err == nil {
				io.CopyN(io.Discard, r, 1<<20)
			}
		}
		walk(img)
	})
}
