package image

import (
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
)

// UDIF chunk types.
const (
	chunkZero    = 0x00000000
	chunkRaw     = 0x00000001
	chunkIgnore  = 0x00000002
	chunkADC     = 0x80000004
	chunkZlib    = 0x80000005
	chunkBzip2   = 0x80000006
	chunkLZFSE   = 0x80000007
	chunkLZMA    = 0x80000008
	chunkComment = 0x7ffffffe
	chunkEnd     = 0xffffffff
)

const (
	udifSector = 512
	// maxChunk is the most one chunk may decompress to. hdiutil's are 1
	// MiB; a bigger claim is damage, and is never allocated.
	maxChunk = 64 << 20
	// maxPlist is the most XML read from a trailer's claim: a 7 GiB disc in
	// 1 MiB chunks has 7,000 40-byte chunk entries, about 400 KiB in base64.
	maxPlist = 16 << 20
	// maxSectors keeps every sector's byte offset within an int64.
	maxSectors = math.MaxInt64 / udifSector
	// maxPlistDepth is far deeper than a UDIF plist nests (five).
	maxPlistDepth = 64
)

// UnsupportedChunkError is a UDIF image with chunks this reader does not
// decompress: ADC, LZFSE, LZMA, or a type it does not know.
type UnsupportedChunkError struct {
	Type uint32
	Name string
}

func (e *UnsupportedChunkError) Error() string {
	return fmt.Sprintf("the UDIF image has %s chunks (type 0x%08x), which longboat does not decompress: convert it with `hdiutil convert -format UDZO` and use the result", e.Name, e.Type)
}

// run is one chunk, placed: absolute sectors, and an absolute file offset.
type run struct {
	sector, sectors int64
	typ             uint32
	off, length     int64
}

// kind names a compressed run's compression.
func (r run) kind() string {
	if r.typ == chunkBzip2 {
		return "bzip2"
	}
	return "zlib"
}

// udif is a UDIF image's disk, decompressed one chunk at a time as its
// ReadAt needs, with the last decompressed chunk cached.
type udif struct {
	r    io.ReaderAt
	size int64
	runs []run // sorted, covering [0, size) exactly

	mu     sync.Mutex
	cached int // the index in runs of buf's chunk, or -1
	buf    []byte
}

func openUDIF(r io.ReaderAt, size int64) (*udif, error) {
	if size < udifSector {
		return nil, fmt.Errorf("%w: a %d-byte file is too small for a UDIF trailer", ErrDamaged, size)
	}
	koly := make([]byte, udifSector)
	if _, err := r.ReadAt(koly, size-udifSector); err != nil {
		return nil, fmt.Errorf("reading the UDIF trailer: %w", err)
	}
	be := binary.BigEndian
	forkOff, forkLen := be.Uint64(koly[24:]), be.Uint64(koly[32:])
	plistOff, plistLen := be.Uint64(koly[216:]), be.Uint64(koly[224:])
	sectors := be.Uint64(koly[492:])
	if !within(forkOff, forkLen, size) {
		return nil, fmt.Errorf("%w: the UDIF data fork (%d bytes at %d) runs past the end of the %d-byte file", ErrDamaged, forkLen, forkOff, size)
	}
	if plistLen == 0 {
		return nil, fmt.Errorf("%w: the UDIF image has no XML plist (an image this old keeps its chunk table in a resource fork)", ErrDamaged)
	}
	if !within(plistOff, plistLen, size) {
		return nil, fmt.Errorf("%w: the UDIF plist (%d bytes at %d) runs past the end of the %d-byte file", ErrDamaged, plistLen, plistOff, size)
	}
	if plistLen > maxPlist {
		return nil, fmt.Errorf("%w: the UDIF plist claims %d bytes, more than the %d this reader allows", ErrDamaged, plistLen, maxPlist)
	}
	if sectors > maxSectors {
		return nil, fmt.Errorf("%w: the UDIF trailer claims %d sectors, more than any disk holds", ErrDamaged, sectors)
	}
	plist := make([]byte, plistLen)
	if _, err := r.ReadAt(plist, int64(plistOff)); err != nil {
		return nil, fmt.Errorf("reading the UDIF plist: %w", err)
	}
	tables, err := blkxTables(plist)
	if err != nil {
		return nil, err
	}

	var runs []run
	for i, t := range tables {
		m, err := parseMish(t)
		if err != nil {
			return nil, fmt.Errorf("blkx entry %d: %w", i, err)
		}
		for _, c := range m.chunks {
			switch c.typ {
			case chunkComment, chunkEnd:
				continue
			case chunkZero, chunkIgnore, chunkRaw, chunkZlib, chunkBzip2:
			default:
				return nil, unsupported(c.typ)
			}
			if c.sectors == 0 {
				continue
			}
			if m.base > maxSectors || c.sector > maxSectors-m.base || c.sectors > maxSectors-m.base-c.sector {
				return nil, fmt.Errorf("%w: blkx entry %d has a chunk at sector %d+%d of %d sectors, past any disk", ErrDamaged, i, m.base, c.sector, c.sectors)
			}
			ru := run{sector: int64(m.base + c.sector), sectors: int64(c.sectors), typ: c.typ}
			if c.typ != chunkZero && c.typ != chunkIgnore {
				// A chunk's offset counts from its table's data offset,
				// which counts from the data fork. hdiutil writes both
				// as 0 (Apple's 10.9 InstallESD.dmg does), so the rule is
				// moot for real images. The tools disagree when they are
				// not: dmg2img ignores the table's, 7-Zip the trailer's.
				// TestChunkOffsetsCountFromTheDataForkAndTheirTable pins
				// this reader's sum; the tool cross-check cannot.
				if m.dataOff > forkLen || c.off > forkLen-m.dataOff || !within(m.dataOff+c.off, c.length, int64(forkLen)) {
					return nil, fmt.Errorf("%w: the UDIF chunk at sector %d stores %d bytes at %d, outside the %d-byte data fork", ErrDamaged, ru.sector, c.length, m.dataOff+c.off, forkLen)
				}
				ru.off, ru.length = int64(forkOff+m.dataOff+c.off), int64(c.length)
				if c.typ == chunkRaw && ru.length != ru.sectors*udifSector {
					return nil, fmt.Errorf("%w: the raw UDIF chunk at sector %d stores %d bytes for its %d sectors", ErrDamaged, ru.sector, ru.length, ru.sectors)
				}
				if c.typ != chunkRaw && c.sectors > maxChunk/udifSector {
					return nil, fmt.Errorf("%w: the UDIF chunk at sector %d claims %d sectors, more than the 64 MiB this reader decompresses at once", ErrDamaged, ru.sector, c.sectors)
				}
			}
			runs = append(runs, ru)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].sector < runs[j].sector })
	next := int64(0)
	for _, ru := range runs {
		switch {
		case ru.sector > next:
			return nil, fmt.Errorf("%w: the UDIF chunk table leaves %s uncovered", ErrDamaged, sectorRange(next, ru.sector-1))
		case ru.sector < next:
			return nil, fmt.Errorf("%w: UDIF chunks overlap at sector %d", ErrDamaged, ru.sector)
		}
		next = ru.sector + ru.sectors
	}
	switch {
	case next < int64(sectors):
		return nil, fmt.Errorf("%w: the UDIF chunk table leaves %s uncovered", ErrDamaged, sectorRange(next, int64(sectors)-1))
	case next > int64(sectors):
		return nil, fmt.Errorf("%w: UDIF chunks run to sector %d, past the trailer's %d sectors", ErrDamaged, next, sectors)
	}
	return &udif{r: r, size: int64(sectors) * udifSector, runs: runs, cached: -1}, nil
}

func sectorRange(first, last int64) string {
	if first == last {
		return fmt.Sprintf("sector %d", first)
	}
	return fmt.Sprintf("sectors %d-%d", first, last)
}

// within says whether length bytes at off lie within limit bytes.
func within(off, length uint64, limit int64) bool {
	return limit >= 0 && off <= uint64(limit) && length <= uint64(limit)-off
}

func unsupported(typ uint32) error {
	name := map[uint32]string{chunkADC: "ADC", chunkLZFSE: "LZFSE", chunkLZMA: "LZMA"}[typ]
	if name == "" {
		name = "unknown"
	}
	return &UnsupportedChunkError{Type: typ, Name: name}
}

type mishChunk struct {
	typ             uint32
	sector, sectors uint64
	off, length     uint64
}

type mish struct {
	base, dataOff uint64
	chunks        []mishChunk
}

// parseMish reads one blkx table: a 204-byte header whose base sector is
// at 8, data offset at 24 and chunk count at 200, then 40-byte chunks.
func parseMish(b []byte) (mish, error) {
	const header, entry = 204, 40
	if len(b) < header {
		return mish{}, fmt.Errorf("%w: its table is %d bytes, too short for a mish header", ErrDamaged, len(b))
	}
	if string(b[:4]) != "mish" {
		return mish{}, fmt.Errorf("%w: its table has no mish signature", ErrDamaged)
	}
	be := binary.BigEndian
	n := uint64(be.Uint32(b[200:]))
	if n > uint64(len(b)-header)/entry {
		return mish{}, fmt.Errorf("%w: its table claims %d chunks, but holds %d bytes", ErrDamaged, n, len(b))
	}
	m := mish{base: be.Uint64(b[8:]), dataOff: be.Uint64(b[24:]), chunks: make([]mishChunk, n)}
	for i := range m.chunks {
		e := b[header+entry*i:]
		m.chunks[i] = mishChunk{
			typ:     be.Uint32(e[0:]),
			sector:  be.Uint64(e[8:]),
			sectors: be.Uint64(e[16:]),
			off:     be.Uint64(e[24:]),
			length:  be.Uint64(e[32:]),
		}
	}
	return m, nil
}

// blkxTables is every resource-fork blkx entry's Data in a UDIF plist.
func blkxTables(b []byte) ([][]byte, error) {
	root, err := parsePlist(b)
	if err != nil {
		return nil, fmt.Errorf("%w: the UDIF plist: %v", ErrDamaged, err)
	}
	top, _ := root.(map[string]any)
	fork, _ := top["resource-fork"].(map[string]any)
	blkx, ok := fork["blkx"].([]any)
	if !ok {
		return nil, fmt.Errorf("%w: the UDIF plist has no resource-fork blkx table", ErrDamaged)
	}
	var tables [][]byte
	for i, e := range blkx {
		d, _ := e.(map[string]any)
		data, ok := d["Data"].([]byte)
		if !ok {
			return nil, fmt.Errorf("%w: the UDIF plist's blkx entry %d has no Data", ErrDamaged, i)
		}
		tables = append(tables, data)
	}
	return tables, nil
}

// parsePlist decodes an XML property list's dicts, arrays, strings and
// data; its other values decode as their text. Depth is bounded.
func parsePlist(b []byte) (any, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Local != "plist" {
				return nil, fmt.Errorf("its root is <%s>, not <plist>", se.Name.Local)
			}
			v, end, err := plistValue(d, 1)
			if err != nil {
				return nil, err
			}
			if end {
				return nil, errors.New("<plist> holds no value")
			}
			if _, end, err := plistValue(d, 1); err != nil {
				return nil, err
			} else if !end {
				return nil, errors.New("<plist> holds more than one value")
			}
			return v, plistEnds(d)
		}
	}
}

// plistEnds says whether the rest of the document is well-formed and
// holds no other element.
func plistEnds(d *xml.Decoder) error {
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return fmt.Errorf("<%s> follows </plist>", se.Name.Local)
		}
	}
}

// plistValue decodes the next value, or says end if its container closed.
func plistValue(d *xml.Decoder, depth int) (v any, end bool, err error) {
	if depth > maxPlistDepth {
		return nil, false, fmt.Errorf("it nests more than %d deep", maxPlistDepth)
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, false, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return nil, true, nil
		case xml.StartElement:
			switch t.Name.Local {
			case "dict":
				m := map[string]any{}
				for {
					k, end, err := plistValue(d, depth+1)
					if err != nil {
						return nil, false, err
					}
					if end {
						return m, false, nil
					}
					key, ok := k.(plistKey)
					if !ok {
						return nil, false, errors.New("a <dict> holds a value where a <key> belongs")
					}
					val, end, err := plistValue(d, depth+1)
					if err != nil {
						return nil, false, err
					}
					if end {
						return nil, false, fmt.Errorf("the <dict>'s key %q has no value", string(key))
					}
					m[string(key)] = val
				}
			case "array":
				var a []any
				for {
					val, end, err := plistValue(d, depth+1)
					if err != nil {
						return nil, false, err
					}
					if end {
						return a, false, nil
					}
					a = append(a, val)
				}
			case "true", "false":
				if err := d.Skip(); err != nil {
					return nil, false, err
				}
				return t.Name.Local == "true", false, nil
			default:
				var s string
				if err := d.DecodeElement(&s, &t); err != nil {
					return nil, false, err
				}
				switch t.Name.Local {
				case "key":
					return plistKey(s), false, nil
				case "data":
					b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
					if err != nil {
						return nil, false, fmt.Errorf("a <data> is not base64: %v", err)
					}
					return b, false, nil
				}
				return s, false, nil
			}
		}
	}
}

type plistKey string

// ReadAt reads the disk, decompressing each chunk it touches.
func (u *udif) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		if pos >= u.size {
			return n, io.EOF
		}
		i := sort.Search(len(u.runs), func(i int) bool {
			return (u.runs[i].sector+u.runs[i].sectors)*udifSector > pos
		})
		ru := u.runs[i]
		in := pos - ru.sector*udifSector
		want := int(min(int64(len(p)-n), ru.sectors*udifSector-in))
		dst := p[n : n+want]
		switch ru.typ {
		case chunkZero, chunkIgnore:
			clear(dst)
		case chunkRaw:
			if m, err := u.r.ReadAt(dst, ru.off+in); m < want {
				if err == nil || err == io.EOF {
					// The table was held to the file's size, so the file
					// shrank since.
					return n + m, fmt.Errorf("%w: the raw UDIF chunk at sector %d ends past the end of the file", ErrDamaged, ru.sector)
				}
				return n + m, fmt.Errorf("reading the raw UDIF chunk at sector %d: %w", ru.sector, err)
			}
		default:
			u.mu.Lock()
			b, err := u.chunk(i)
			if err == nil {
				copy(dst, b[in:])
			}
			u.mu.Unlock()
			if err != nil {
				return n, err
			}
		}
		n += want
	}
	return n, nil
}

// chunk is run i, decompressed, from the cache if it is there. u.mu is
// held.
func (u *udif) chunk(i int) ([]byte, error) {
	if u.cached == i {
		return u.buf, nil
	}
	ru := u.runs[i]
	src := &readErr{r: io.NewSectionReader(u.r, ru.off, ru.length)}
	// fail says why decompressing failed: the file's read error, as it is,
	// if there was one, so a failing drive is never called damage.
	fail := func(damage error) error {
		if src.err != nil {
			return fmt.Errorf("reading the %s chunk at sector %d: %w", ru.kind(), ru.sector, src.err)
		}
		return damage
	}
	var dec io.Reader
	switch ru.typ {
	case chunkZlib:
		z, err := zlib.NewReader(src)
		if err != nil {
			return nil, fail(fmt.Errorf("%w: the zlib chunk at sector %d: %v", ErrDamaged, ru.sector, err))
		}
		dec = z
	case chunkBzip2:
		dec = bzip2.NewReader(src)
	}
	want := ru.sectors * udifSector
	if int64(cap(u.buf)) < want {
		u.buf = make([]byte, want)
	}
	u.cached = -1
	buf := u.buf[:want]
	got, err := io.ReadFull(dec, buf)
	switch {
	case err == io.ErrUnexpectedEOF || err == io.EOF:
		return nil, fail(fmt.Errorf("%w: the %s chunk at sector %d decompresses to %d bytes, not its %d sectors' %d", ErrDamaged, ru.kind(), ru.sector, got, ru.sectors, want))
	case err != nil:
		return nil, fail(fmt.Errorf("%w: the %s chunk at sector %d: %v", ErrDamaged, ru.kind(), ru.sector, err))
	}
	// The stream must end here, and its checksum hold.
	switch _, err := io.ReadFull(dec, make([]byte, 1)); err {
	case io.EOF:
	case nil:
		return nil, fail(fmt.Errorf("%w: the %s chunk at sector %d decompresses to more than its %d sectors' %d bytes", ErrDamaged, ru.kind(), ru.sector, ru.sectors, want))
	default:
		return nil, fail(fmt.Errorf("%w: the %s chunk at sector %d: %v", ErrDamaged, ru.kind(), ru.sector, err))
	}
	u.buf, u.cached = buf, i
	return buf, nil
}

// readErr is r, remembering any error but EOF that it gave: a
// decompressor reports its source's errors as its own, or as corrupt
// input.
type readErr struct {
	r   io.Reader
	err error
}

func (e *readErr) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}
