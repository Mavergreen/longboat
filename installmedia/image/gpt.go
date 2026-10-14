package image

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

const gptSector = 512

// appleHFSType is the GPT type 48465300-0000-11AA-AA11-00306543ECAC in
// its on-disk order: the first three fields little-endian.
var appleHFSType = []byte{0x00, 0x53, 0x46, 0x48, 0x00, 0x00, 0xaa, 0x11, 0xaa, 0x11, 0x00, 0x30, 0x65, 0x43, 0xec, 0xac}

// maxGPTTable is far more partition entries than a disk has: 128 entries
// of 128 bytes is the usual 16 KiB.
const maxGPTTable = 1 << 20

// gptHFS is the byte range of the first Apple HFS partition in a GPT disk
// of 512-byte sectors, from its primary header and entries, both held to
// their CRC-32s.
func gptHFS(r io.ReaderAt, size int64) (off, length int64, err error) {
	le := binary.LittleEndian
	h := make([]byte, gptSector)
	if _, err := r.ReadAt(h, gptSector); err != nil {
		return 0, 0, fmt.Errorf("reading the GPT header: %w", err)
	}
	hsize := le.Uint32(h[12:])
	if hsize < 92 || hsize > gptSector {
		return 0, 0, fmt.Errorf("%w: the GPT header claims to be %d bytes", ErrDamaged, hsize)
	}
	sum := le.Uint32(h[16:])
	clear(h[16:20])
	if crc32.ChecksumIEEE(h[:hsize]) != sum {
		return 0, 0, fmt.Errorf("%w: the GPT header checksum is wrong", ErrDamaged)
	}
	at, count, esize := le.Uint64(h[72:]), uint64(le.Uint32(h[80:])), uint64(le.Uint32(h[84:]))
	if esize < 128 || esize%8 != 0 || count*esize > maxGPTTable {
		return 0, 0, fmt.Errorf("%w: the GPT claims %d entries of %d bytes", ErrDamaged, count, esize)
	}
	if at > uint64(size)/gptSector || int64(at*gptSector+count*esize) > size {
		return 0, 0, fmt.Errorf("%w: the GPT's entries (at sector %d) run past the end of the %d-byte image", ErrDamaged, at, size)
	}
	table := make([]byte, count*esize)
	if _, err := r.ReadAt(table, int64(at*gptSector)); err != nil {
		return 0, 0, fmt.Errorf("reading the GPT's entries: %w", err)
	}
	if crc32.ChecksumIEEE(table) != le.Uint32(h[88:]) {
		return 0, 0, fmt.Errorf("%w: the GPT entries checksum is wrong", ErrDamaged)
	}
	for i := uint64(0); i < count; i++ {
		e := table[i*esize:]
		if !bytes.Equal(e[:16], appleHFSType) {
			continue
		}
		first, last := le.Uint64(e[32:]), le.Uint64(e[40:])
		if last < first || last >= uint64(size)/gptSector {
			return 0, 0, fmt.Errorf("%w: the Apple HFS partition (sectors %d-%d) runs past the end of the %d-byte image", ErrDamaged, first, last, size)
		}
		return inside(int64(first*gptSector), int64((last-first+1)*gptSector), size, "the Apple HFS partition")
	}
	return 0, 0, errors.New("the GUID partition table has no Apple HFS partition")
}
