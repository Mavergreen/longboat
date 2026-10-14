package fixture

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
)

// Chunk is how a UDIF fixture stores its chunks.
type Chunk int

const (
	Raw   Chunk = iota // every chunk stored as it is (UDRO's 0x1)
	Zero               // all-zero chunks stored as no bytes, alternately 0x0 and 0x2; the rest raw
	Zlib               // every chunk zlib (UDZO's 0x80000005)
	Bzip2              // every chunk bzip2 (UDBZ's 0x80000006)
	LZFSE              // the type only (0x80000007), over raw bytes: for refusal tests
)

func (c Chunk) String() string {
	return [...]string{"Raw", "Zero", "Zlib", "Bzip2", "LZFSE"}[c]
}

// UDIF chunk types.
const (
	TypeZero    = 0x00000000
	TypeRaw     = 0x00000001
	TypeIgnore  = 0x00000002
	TypeADC     = 0x80000004
	TypeZlib    = 0x80000005
	TypeBzip2   = 0x80000006
	TypeLZFSE   = 0x80000007
	TypeLZMA    = 0x80000008
	TypeComment = 0x7ffffffe
	TypeEnd     = 0xffffffff
)

// Layout is a UDIF image before it is written. A test that needs a
// damaged image edits it, then calls Bytes.
type Layout struct {
	Data    []byte // the data fork: every chunk's stored bytes
	Blocks  []Block
	Sectors uint64 // the koly trailer's sector count
	// DataForkOffset is where the data fork starts in the file; Bytes
	// fills the bytes before it with 0xa5. hdiutil writes 0.
	DataForkOffset uint64
}

// Block is one blkx entry's mish table. Bytes adds its terminator.
type Block struct {
	Name   string
	Base   uint64 // the block's first sector; its chunks count from it
	Chunks []ChunkEntry
	// DataOffset is the table's data offset, into the data fork. Bytes
	// writes each chunk's offset relative to it (0 for an entry before
	// it, such as a comment). hdiutil writes 0.
	DataOffset uint64
}

// ChunkEntry is one 40-byte chunk entry. Offset is into the data fork,
// whatever its Block's DataOffset.
type ChunkEntry struct {
	Type            uint32
	Sector, Sectors uint64
	Offset, Length  uint64
}

// chunksPerBlock splits a fixture's chunks over several blkx entries, as
// hdiutil does one per partition, so a reader must honour each block's
// base sector.
const chunksPerBlock = 3

// UDIF is disk as a UDIF image whose chunks are chunkSectors sectors
// each, stored as c says. disk is padded with zeros to whole sectors.
func UDIF(disk []byte, c Chunk, chunkSectors int) []byte {
	return UDIFLayout(disk, c, chunkSectors).Bytes()
}

// UDIFLayout is UDIF's image before it is written.
func UDIFLayout(disk []byte, c Chunk, chunkSectors int) *Layout {
	if chunkSectors < 1 {
		panic("fixture.UDIF: chunkSectors must be at least 1")
	}
	if r := len(disk) % sector; r != 0 {
		disk = append(append([]byte(nil), disk...), make([]byte, sector-r)...)
	}
	l := &Layout{Sectors: uint64(len(disk) / sector)}
	zeros := 0
	for s := uint64(0); s < l.Sectors; s += uint64(chunkSectors) {
		n := min(uint64(chunkSectors), l.Sectors-s)
		raw := disk[s*sector : (s+n)*sector]
		if len(l.Blocks) == 0 || len(l.Blocks[len(l.Blocks)-1].Chunks) == chunksPerBlock+1 {
			l.Blocks = append(l.Blocks, Block{
				Name:   fmt.Sprintf("fixture (Apple_HFS : %d)", len(l.Blocks)),
				Base:   s,
				Chunks: []ChunkEntry{{Type: TypeComment}},
			})
		}
		b := &l.Blocks[len(l.Blocks)-1]
		e := ChunkEntry{Sector: s - b.Base, Sectors: n, Offset: uint64(len(l.Data))}
		var stored []byte
		switch c {
		case Raw:
			e.Type, stored = TypeRaw, raw
		case Zero:
			if bytes.Count(raw, []byte{0}) == len(raw) {
				e.Type = []uint32{TypeZero, TypeIgnore}[zeros%2]
				zeros++
			} else {
				e.Type, stored = TypeRaw, raw
			}
		case Zlib:
			var z bytes.Buffer
			zw := zlib.NewWriter(&z)
			zw.Write(raw)
			zw.Close()
			e.Type, stored = TypeZlib, z.Bytes()
		case Bzip2:
			e.Type, stored = TypeBzip2, Bzip2Compress(raw)
		case LZFSE:
			e.Type, stored = TypeLZFSE, raw
		default:
			panic(fmt.Sprintf("fixture.UDIF: no chunk kind %d", c))
		}
		e.Length = uint64(len(stored))
		l.Data = append(l.Data, stored...)
		b.Chunks = append(b.Chunks, e)
	}
	return l
}

// Bytes writes the image: the data fork, the XML plist, then the koly
// trailer.
func (l *Layout) Bytes() []byte {
	var xml strings.Builder
	xml.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>resource-fork</key>
	<dict>
		<key>blkx</key>
		<array>
`)
	for i, b := range l.Blocks {
		mish := l.mish(i, b)
		enc := base64.StdEncoding.EncodeToString(mish)
		fmt.Fprintf(&xml, "\t\t\t<dict>\n\t\t\t\t<key>Attributes</key>\n\t\t\t\t<string>0x0050</string>\n")
		fmt.Fprintf(&xml, "\t\t\t\t<key>CFName</key>\n\t\t\t\t<string>%s</string>\n", b.Name)
		xml.WriteString("\t\t\t\t<key>Data</key>\n\t\t\t\t<data>\n")
		for len(enc) > 0 {
			n := min(len(enc), 52)
			fmt.Fprintf(&xml, "\t\t\t\t%s\n", enc[:n])
			enc = enc[n:]
		}
		xml.WriteString("\t\t\t\t</data>\n")
		fmt.Fprintf(&xml, "\t\t\t\t<key>ID</key>\n\t\t\t\t<string>%d</string>\n", i-1)
		fmt.Fprintf(&xml, "\t\t\t\t<key>Name</key>\n\t\t\t\t<string>%s</string>\n\t\t\t</dict>\n", b.Name)
	}
	xml.WriteString("\t\t</array>\n\t</dict>\n</dict>\n</plist>\n")

	img := bytes.Repeat([]byte{0xa5}, int(l.DataForkOffset))
	img = append(img, l.Data...)
	xmlOff := uint64(len(img))
	img = append(img, xml.String()...)
	koly := make([]byte, 512)
	copy(koly, "koly")
	be := binary.BigEndian
	be.PutUint32(koly[4:], 4)   // version
	be.PutUint32(koly[8:], 512) // header size
	be.PutUint32(koly[12:], 1)  // flags: flattened
	be.PutUint64(koly[24:], l.DataForkOffset)
	be.PutUint64(koly[32:], uint64(len(l.Data)))
	be.PutUint32(koly[56:], 1) // segment number
	be.PutUint32(koly[60:], 1) // segment count
	be.PutUint64(koly[216:], xmlOff)
	be.PutUint64(koly[224:], uint64(xml.Len()))
	be.PutUint32(koly[488:], 1) // image variant: device image
	be.PutUint64(koly[492:], l.Sectors)
	return append(img, koly...)
}

// mish is block i's table: a 204-byte header, then its chunks and a
// terminator, 40 bytes each.
func (l *Layout) mish(i int, b Block) []byte {
	var sectors, end uint64
	for _, c := range b.Chunks {
		sectors = max(sectors, c.Sector+c.Sectors)
		end = max(end, c.Offset+c.Length)
	}
	chunks := append(append([]ChunkEntry(nil), b.Chunks...), ChunkEntry{Type: TypeEnd, Sector: sectors, Offset: end})
	m := make([]byte, 204+40*len(chunks))
	be := binary.BigEndian
	copy(m, "mish")
	be.PutUint32(m[4:], 1)
	be.PutUint64(m[8:], b.Base)
	be.PutUint64(m[16:], sectors)
	be.PutUint64(m[24:], b.DataOffset)
	be.PutUint32(m[32:], 0x208)     // buffers needed
	be.PutUint32(m[36:], uint32(i)) // block descriptor
	be.PutUint32(m[200:], uint32(len(chunks)))
	for j, c := range chunks {
		e := m[204+40*j:]
		be.PutUint32(e[0:], c.Type)
		be.PutUint64(e[8:], c.Sector)
		be.PutUint64(e[16:], c.Sectors)
		if c.Offset >= b.DataOffset {
			be.PutUint64(e[24:], c.Offset-b.DataOffset)
		}
		be.PutUint64(e[32:], c.Length)
	}
	return m
}
