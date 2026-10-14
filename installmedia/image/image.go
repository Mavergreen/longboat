// Package image reads an installer disc image, whatever made it, and finds
// the HFS+ volume inside: a retail DVD ripped to an ISO or a Disk Utility
// "DVD/CD master" (an Apple Partition Map), a GUID-partitioned disk, a
// UDIF .dmg (raw, zlib or bzip2 chunks), or the volume alone. It is pure
// Go, so it reads a disc on any host.
//
// A damaged or hostile image gives an error wrapping ErrDamaged, never a
// panic, a hang or an unbounded allocation.
package image

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrDamaged is wrapped by every error that says an image's own
// structures are inconsistent: truncated, out of range or absurd.
var ErrDamaged = errors.New("damaged disc image")

// Format is how an image holds its volume.
type Format int

const (
	APM     Format = iota + 1 // an Apple Partition Map: a ripped disc
	GPT                       // a GUID Partition Table: hdiutil's default
	UDIF                      // a .dmg, read here chunk by chunk
	BareHFS                   // the HFS+ volume alone
)

func (f Format) String() string {
	switch f {
	case APM:
		return "Apple Partition Map"
	case GPT:
		return "GUID Partition Table"
	case UDIF:
		return "UDIF disk image"
	case BareHFS:
		return "bare HFS+ volume"
	}
	return fmt.Sprintf("Format(%d)", int(f))
}

// errHFSWrapper is a Mac OS Standard volume that embeds an HFS+ one, as
// Mac OS 8.1-era discs do; no Mac OS X install disc is one.
var errHFSWrapper = errors.New("an HFS wrapper (a Mac OS Standard volume, signature BD, embedding an HFS+ one), which this reader does not unwrap: no Mac OS X install disc is one")

// Detect says which format r is, from its content and never a name: a
// UDIF image ends in a 512-byte trailer starting "koly"; an APM's driver
// descriptor starts "ER"; a GPT's header, at byte 512, starts "EFI PART";
// an HFS+ (or HFSX) volume's header, at byte 1024, starts "H+" ("HX").
func Detect(r io.ReaderAt, size int64) (Format, error) {
	head := make([]byte, 4)
	if _, err := r.ReadAt(head, 0); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, fmt.Errorf("not a disc image longboat reads: it is %d bytes, too few to hold any format's signature", size)
		}
		return 0, fmt.Errorf("reading the image's first bytes: %w", err)
	}
	if size >= 512 && hasAt(r, size-512, "koly") {
		return UDIF, nil
	}
	if bytes.HasPrefix(head, []byte("ER")) {
		return APM, nil
	}
	if hasAt(r, 512, "EFI PART") {
		return GPT, nil
	}
	if hasAt(r, 1024, "H+") || hasAt(r, 1024, "HX") {
		return BareHFS, nil
	}
	if hasAt(r, 1024, "BD") {
		return 0, fmt.Errorf("the image is %w", errHFSWrapper)
	}
	return 0, fmt.Errorf("not a disc image longboat reads: it starts %x, which is no Apple Partition Map, GUID Partition Table, UDIF image or HFS+ volume", head)
}

func hasAt(r io.ReaderAt, off int64, sig string) bool {
	b := make([]byte, len(sig))
	_, err := r.ReadAt(b, off)
	return err == nil && string(b) == sig
}

// HFSRange is the byte range of the HFS+ volume in r: the first Apple_HFS
// partition of an APM, the first Apple HFS partition of a GPT, or all of a
// bare volume. A UDIF image's volume is compressed, so it is an error:
// OpenVolume decompresses it.
func HFSRange(r io.ReaderAt, size int64) (off, length int64, f Format, err error) {
	if f, err = Detect(r, size); err != nil {
		return 0, 0, 0, err
	}
	switch f {
	case APM:
		off, length, err = apmHFS(r, size)
	case GPT:
		off, length, err = gptHFS(r, size)
	case BareHFS:
		off, length = 0, size
	case UDIF:
		err = errors.New("a UDIF disk image holds its volume in chunks, most of them compressed: decompress it first (OpenVolume does)")
	}
	if err != nil {
		return 0, 0, f, err
	}
	return off, length, f, nil
}

// maxAPMEntries is more partitions than any disc's map holds; a count
// past it is damage.
const maxAPMEntries = 256

// apmHFS is the byte range of the first Apple_HFS partition in an APM
// image. The map's block size is the driver descriptor's (2048 on a
// retail DVD), and its entries follow at block 1.
func apmHFS(r io.ReaderAt, size int64) (off, length int64, err error) {
	ddm := make([]byte, 4)
	if _, err := r.ReadAt(ddm, 0); err != nil {
		return 0, 0, err
	}
	bs := int64(binary.BigEndian.Uint16(ddm[2:4]))
	if bs == 0 || bs%512 != 0 {
		return 0, 0, fmt.Errorf("%w: the partition map's driver descriptor gives a block size of %d", ErrDamaged, bs)
	}
	entry := make([]byte, 80)
	count := int64(1)
	for i := int64(1); i <= count; i++ {
		if i*bs+int64(len(entry)) > size {
			return 0, 0, fmt.Errorf("%w: partition map entry %d lies past the end of the %d-byte image", ErrDamaged, i, size)
		}
		if _, err := r.ReadAt(entry, i*bs); err != nil {
			return 0, 0, fmt.Errorf("reading partition map entry %d: %w", i, err)
		}
		if string(entry[:2]) != "PM" {
			return 0, 0, fmt.Errorf("%w: partition map entry %d has no PM signature", ErrDamaged, i)
		}
		count = int64(binary.BigEndian.Uint32(entry[4:8]))
		if count > maxAPMEntries {
			return 0, 0, fmt.Errorf("%w: the partition map claims %d entries", ErrDamaged, count)
		}
		if string(bytes.TrimRight(entry[48:80], "\x00")) == "Apple_HFS" {
			start := int64(binary.BigEndian.Uint32(entry[8:12]))
			blocks := int64(binary.BigEndian.Uint32(entry[12:16]))
			return inside(start*bs, blocks*bs, size, "the Apple_HFS partition")
		}
	}
	return 0, 0, errors.New("the partition map has no Apple_HFS partition")
}

// inside is off and length, if they lie within an image of size bytes.
// Callers keep both under 2^62, so their sum cannot overflow.
func inside(off, length, size int64, what string) (int64, int64, error) {
	if length <= 0 {
		return 0, 0, fmt.Errorf("%w: %s is empty", ErrDamaged, what)
	}
	if off+length > size {
		return 0, 0, fmt.Errorf("%w: %s (%d bytes at %d) runs past the end of the %d-byte image", ErrDamaged, what, length, off, size)
	}
	return off, length, nil
}

// Volume is the HFS+ volume inside an image. Format is the image's
// container, not the volume's. Its ReadAt is safe for concurrent use.
type Volume struct {
	io.ReaderAt
	Size   int64
	Format Format

	file *os.File
}

// Close closes the image file.
func (v *Volume) Close() error {
	if v.file == nil {
		return nil
	}
	err := v.file.Close()
	v.file = nil
	return err
}

// OpenVolume opens the HFS+ volume in the image at path: an APM, a GPT, a
// UDIF image (decompressed on demand, a chunk at a time) or a bare
// volume. A device, such as an optical drive, reads as its image would.
func OpenVolume(path string) (*Volume, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	size, err := imageSize(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	v, err := openVolume(f, size)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	v.file = f
	return v, nil
}

func imageSize(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	switch m := st.Mode(); {
	case m.IsRegular():
		return st.Size(), nil
	case m&os.ModeDevice != 0:
		return f.Seek(0, io.SeekEnd)
	}
	return 0, fmt.Errorf("is not a file or a device (it is a %s)", st.Mode().Type())
}

func openVolume(r io.ReaderAt, size int64) (*Volume, error) {
	format, err := Detect(r, size)
	if err != nil {
		return nil, err
	}
	disk, diskSize := r, size
	if format == UDIF {
		u, err := openUDIF(r, size)
		if err != nil {
			return nil, err
		}
		disk, diskSize = u, u.size
		inner, err := Detect(disk, diskSize)
		if err != nil {
			return nil, fmt.Errorf("the UDIF image's disk: %w", err)
		}
		if inner == UDIF {
			return nil, errors.New("a UDIF image inside a UDIF image: decompress the outer one first")
		}
	}
	off, n, _, err := HFSRange(disk, diskSize)
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 2)
	if n < 1024+2 {
		return nil, fmt.Errorf("%w: the HFS+ partition is %d bytes, too small for a volume header", ErrDamaged, n)
	}
	if _, err := disk.ReadAt(sig, off+1024); err != nil {
		return nil, fmt.Errorf("reading the volume header: %w", err)
	}
	switch string(sig) {
	case "H+", "HX":
	case "BD":
		return nil, fmt.Errorf("the partition holds %w", errHFSWrapper)
	default:
		return nil, fmt.Errorf("the partition holds no HFS+ volume: its header at byte 1024 starts %x, not H+", sig)
	}
	return &Volume{ReaderAt: io.NewSectionReader(disk, off, n), Size: n, Format: format}, nil
}
