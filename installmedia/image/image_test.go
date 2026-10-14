package image

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Mavergreen/longboat/installmedia/internal/fixture"
)

const apmBlock = 2048

// hfsVolume is a bare volume: HFS+'s signature at byte 1024, then a
// recognizable tail.
func hfsVolume(size int) []byte {
	v := make([]byte, size)
	copy(v[1024:], "H+")
	copy(v[size-8:], "THE-END!")
	return v
}

// testVolume is a volume whose bytes are worth comparing: HFS+'s
// signature, low-entropy data that compresses, runs that bzip2 codes, and
// bands of zero sectors that a Zero fixture stores as no bytes.
func testVolume() []byte {
	rng := rand.New(rand.NewPCG(6, 9))
	v := make([]byte, 96*1024)
	for i := range v {
		switch (i / 4096) % 3 {
		case 0:
			v[i] = byte(rng.Uint32() % 5)
		case 2:
			v[i] = byte(i / 300)
		}
	}
	copy(v[1024:], "H+\x00\x04")
	copy(v[len(v)-8:], "THE-END!")
	return v
}

func write(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func detect(t *testing.T, b []byte) (Format, error) {
	t.Helper()
	return Detect(bytes.NewReader(b), int64(len(b)))
}

func hfsRange(b []byte) (int64, int64, Format, error) {
	return HFSRange(bytes.NewReader(b), int64(len(b)))
}

func TestDetectAPM(t *testing.T) {
	if f, err := detect(t, fixture.APM(hfsVolume(8192))); err != nil || f != APM {
		t.Fatalf("Detect = %v, %v; want APM", f, err)
	}
}

func TestDetectBareHFS(t *testing.T) {
	if f, err := detect(t, hfsVolume(8192)); err != nil || f != BareHFS {
		t.Fatalf("Detect = %v, %v; want BareHFS", f, err)
	}
}

func TestDetectUDIF(t *testing.T) {
	b := make([]byte, 4096)
	copy(b[len(b)-512:], "koly")
	if f, err := detect(t, b); err != nil || f != UDIF {
		t.Fatalf("Detect = %v, %v; want UDIF", f, err)
	}
}

func TestDetectRefusesUnknown(t *testing.T) {
	_, err := detect(t, make([]byte, 4096))
	if err == nil || !strings.Contains(err.Error(), "00000000") {
		t.Fatalf("err = %v; want one naming the first bytes, 00000000", err)
	}
}

func TestDetectGPT(t *testing.T) {
	if f, err := detect(t, fixture.GPT(hfsVolume(8192))); err != nil || f != GPT {
		t.Fatalf("Detect = %v, %v; want GPT", f, err)
	}
}

func TestDetectNamesAnHFSWrapper(t *testing.T) {
	v := make([]byte, 8192)
	copy(v[1024:], "BD")
	_, err := detect(t, v)
	if err == nil || !strings.Contains(err.Error(), "HFS wrapper") {
		t.Fatalf("err = %v; want one naming an HFS wrapper", err)
	}
}

func TestFormatsSayWhatTheyAre(t *testing.T) {
	for f, want := range map[Format]string{
		APM:       "Apple Partition Map",
		GPT:       "GUID Partition Table",
		UDIF:      "UDIF disk image",
		BareHFS:   "bare HFS+ volume",
		Format(9): "Format(9)",
	} {
		if f.String() != want {
			t.Errorf("Format %d says %q; want %q", int(f), f, want)
		}
	}
}

func TestHFSRangeFindsTheHFSEntry(t *testing.T) {
	vol := hfsVolume(3 * apmBlock)
	off, n, f, err := hfsRange(fixture.APM(vol))
	if err != nil {
		t.Fatal(err)
	}
	if off != 6*apmBlock || n != int64(len(vol)) || f != APM {
		t.Fatalf("HFSRange = %d, %d, %v; want %d, %d, APM", off, n, f, 6*apmBlock, len(vol))
	}
}

func TestHFSRangeRefusesNoHFS(t *testing.T) {
	img := fixture.APM(hfsVolume(apmBlock))
	copy(img[3*apmBlock+48:3*apmBlock+80], make([]byte, 32))
	copy(img[3*apmBlock+48:], "Apple_Free")
	_, _, _, err := hfsRange(img)
	if err == nil || !strings.Contains(err.Error(), "Apple_HFS") {
		t.Fatalf("err = %v; want one naming Apple_HFS", err)
	}
}

func TestHFSRangeFindsTheGPTPartition(t *testing.T) {
	vol := hfsVolume(8 * 512)
	img := fixture.GPT(vol)
	off, n, f, err := hfsRange(img)
	if err != nil {
		t.Fatal(err)
	}
	if f != GPT || n != int64(len(vol)) || !bytes.Equal(img[off:off+n], vol) {
		t.Fatalf("HFSRange = %d, %d, %v; want the Apple HFS partition, %d bytes", off, n, f, len(vol))
	}
}

func TestHFSRangeOfABareVolumeIsAllOfIt(t *testing.T) {
	vol := hfsVolume(8192)
	if off, n, f, err := hfsRange(vol); err != nil || off != 0 || n != 8192 || f != BareHFS {
		t.Fatalf("HFSRange = %d, %d, %v, %v; want 0, 8192, BareHFS", off, n, f, err)
	}
}

func TestHFSRangeTellsAUDIFImageToDecompressFirst(t *testing.T) {
	_, _, f, err := hfsRange(fixture.UDIF(fixture.APM(hfsVolume(8192)), fixture.Zlib, 7))
	if f != UDIF || err == nil || !strings.Contains(err.Error(), "decompress it first") {
		t.Fatalf("HFSRange = %v, %v; want UDIF and an error saying to decompress it first", f, err)
	}
}

// readsLike holds v to vol: every byte, read in odd-sized pieces at
// unaligned offsets, then at random, then at and past the end.
func readsLike(t *testing.T, v io.ReaderAt, size int64, vol []byte) {
	t.Helper()
	if size != int64(len(vol)) {
		t.Fatalf("Size = %d; want %d", size, len(vol))
	}
	sizes := []int{1, 511, 513, 3583, 4097, 7*512 + 1, 10000}
	for off, i := int64(0), 0; off < size; i++ {
		buf := make([]byte, sizes[i%len(sizes)])
		n, err := v.ReadAt(buf, off)
		want := min(int64(len(buf)), size-off)
		if int64(n) != want || (want < int64(len(buf)) && err != io.EOF) || (want == int64(len(buf)) && err != nil) {
			t.Fatalf("ReadAt(%d bytes, %d) = %d, %v; want %d", len(buf), off, n, err, want)
		}
		if !bytes.Equal(buf[:n], vol[off:off+want]) {
			t.Fatalf("ReadAt(%d bytes, %d) read the wrong bytes", len(buf), off)
		}
		off += want
	}
	rng := rand.New(rand.NewPCG(1, 1))
	for range 300 {
		off := rng.Int64N(size)
		buf := make([]byte, 1+rng.IntN(9000))
		n, _ := v.ReadAt(buf, off)
		if want := min(int64(len(buf)), size-off); int64(n) != want || !bytes.Equal(buf[:n], vol[off:off+want]) {
			t.Fatalf("ReadAt(%d bytes, %d) = %d bytes, or the wrong ones; want %d", len(buf), off, n, want)
		}
	}
	if n, err := v.ReadAt(make([]byte, 10), size); n != 0 || err != io.EOF {
		t.Fatalf("ReadAt at the end = %d, %v; want 0, EOF", n, err)
	}
	if _, err := v.ReadAt(make([]byte, 10), -1); err == nil {
		t.Fatal("ReadAt at -1 succeeded")
	}
}

func TestOpenVolumeReadsEveryContainer(t *testing.T) {
	vol := testVolume()
	for _, c := range []struct {
		name   string
		img    []byte
		format Format
	}{
		{"bare", vol, BareHFS},
		{"APM", fixture.APM(vol), APM},
		{"GPT", fixture.GPT(vol), GPT},
		{"UDIF raw", fixture.UDIF(fixture.APM(vol), fixture.Raw, 7), UDIF},
		{"UDIF zero", fixture.UDIF(fixture.APM(vol), fixture.Zero, 7), UDIF},
		{"UDIF zlib", fixture.UDIF(fixture.APM(vol), fixture.Zlib, 7), UDIF},
		{"UDIF bzip2", fixture.UDIF(fixture.APM(vol), fixture.Bzip2, 7), UDIF},
		{"UDIF zlib of a GPT disk", fixture.UDIF(fixture.GPT(vol), fixture.Zlib, 2048), UDIF},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, err := OpenVolume(write(t, "image", c.img))
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			if v.Format != c.format {
				t.Errorf("Format = %v; want %v", v.Format, c.format)
			}
			readsLike(t, v, v.Size, vol)
		})
	}
}

func TestAVolumeReadsConcurrently(t *testing.T) {
	vol := testVolume()
	v, err := OpenVolume(write(t, "image.dmg", fixture.UDIF(fixture.APM(vol), fixture.Bzip2, 5)))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := range 8 {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(g), 7))
			for range 100 {
				off := rng.Int64N(v.Size)
				buf := make([]byte, 1+rng.IntN(5000))
				n, err := v.ReadAt(buf, off)
				want := min(int64(len(buf)), v.Size-off)
				if int64(n) != want || (err != nil && !(err == io.EOF && want < int64(len(buf)))) {
					errs <- fmt.Errorf("ReadAt(%d bytes, %d) = %d, %v; want %d", len(buf), off, n, err, want)
					return
				}
				if !bytes.Equal(buf[:n], vol[off:off+want]) {
					errs <- fmt.Errorf("ReadAt(%d bytes, %d) read the wrong bytes", len(buf), off)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestOpenVolumeRefusesWhatIsNoVolume(t *testing.T) {
	wrapper := make([]byte, 8192)
	copy(wrapper[1024:], "BD")
	inner := make([]byte, 4096)
	copy(inner[len(inner)-512:], "koly")
	for name, c := range map[string]struct {
		img  []byte
		want string
	}{
		"an HFS wrapper in a partition": {fixture.APM(wrapper), "HFS wrapper"},
		"a partition with no volume":    {fixture.APM(make([]byte, 8192)), "no HFS+ volume"},
		"a UDIF image of a UDIF image":  {fixture.UDIF(inner, fixture.Raw, 4), "UDIF image inside"},
		"an empty file":                 {nil, "not a disc image longboat reads: it is 0 bytes"},
		"a file of three bytes":         {[]byte("ER!"), "not a disc image longboat reads: it is 3 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := OpenVolume(write(t, "image", c.img))
			if err == nil {
				v.Close()
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v; want one containing %q", err, c.want)
			}
		})
	}
	if _, err := OpenVolume(t.TempDir()); err == nil {
		t.Error("OpenVolume opened a directory")
	}
	if _, err := OpenVolume(filepath.Join(t.TempDir(), "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v; want ErrNotExist", err)
	}
}

// TestChunkOffsetsCountFromTheDataForkAndTheirTable pins where a chunk's
// bytes are: the trailer's data fork offset, plus its table's data offset,
// plus its own. hdiutil writes both as 0, and the tools disagree (dmg2img
// ignores the table's, 7-Zip the trailer's), so only this test holds it.
func TestChunkOffsetsCountFromTheDataForkAndTheirTable(t *testing.T) {
	vol := testVolume()
	for _, c := range []fixture.Chunk{fixture.Raw, fixture.Zlib, fixture.Bzip2} {
		t.Run(c.String(), func(t *testing.T) {
			l := fixture.UDIFLayout(fixture.APM(vol), c, 7)
			const pad = 3000
			l.Data = append(bytes.Repeat([]byte{0xa5}, pad), l.Data...)
			for i := range l.Blocks {
				b := &l.Blocks[i]
				for j := range b.Chunks {
					b.Chunks[j].Offset += pad
				}
				b.DataOffset = b.Chunks[1].Offset - uint64(i)
			}
			l.DataForkOffset = 4096
			img := l.Bytes()
			if got := binary.BigEndian.Uint64(img[len(img)-512+24:]); got != 4096 {
				t.Fatalf("the trailer's data fork offset is %d; want 4096", got)
			}
			v, err := OpenVolume(write(t, "image.dmg", img))
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			readsLike(t, v, v.Size, vol)
		})
	}
}

// failing is r, except that a read touching [from, to) gives err.
type failing struct {
	r        io.ReaderAt
	from, to int64
	err      error
}

func (f failing) ReadAt(p []byte, off int64) (int, error) {
	if off < f.to && off+int64(len(p)) > f.from {
		return 0, f.err
	}
	return f.r.ReadAt(p, off)
}

func TestReadErrorsAreNotDamage(t *testing.T) {
	disk := fixture.APM(testVolume())
	for _, c := range []fixture.Chunk{fixture.Raw, fixture.Zlib, fixture.Bzip2} {
		t.Run(c.String(), func(t *testing.T) {
			l := fixture.UDIFLayout(disk, c, 7)
			b := l.Blocks[len(l.Blocks)-1]
			last := b.Chunks[len(b.Chunks)-1]
			img := l.Bytes()
			from := int64(last.Offset)
			r := failing{r: bytes.NewReader(img), from: from, to: from + int64(last.Length), err: syscall.EIO}
			err := openAndReadAll(t, r, int64(len(img)))
			if !errors.Is(err, syscall.EIO) || errors.Is(err, ErrDamaged) {
				t.Fatalf("err = %v; want EIO, and not ErrDamaged", err)
			}
		})
	}
}

func TestUnsupportedChunksAreNamed(t *testing.T) {
	disk := fixture.APM(hfsVolume(8192))
	for _, c := range []struct {
		typ  uint32
		name string
	}{
		{fixture.TypeLZFSE, "LZFSE"},
		{fixture.TypeADC, "ADC"},
		{fixture.TypeLZMA, "LZMA"},
		{0x80000099, "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := fixture.UDIFLayout(disk, fixture.LZFSE, 7)
			for i := range l.Blocks {
				for j := range l.Blocks[i].Chunks {
					if l.Blocks[i].Chunks[j].Type == fixture.TypeLZFSE {
						l.Blocks[i].Chunks[j].Type = c.typ
					}
				}
			}
			_, err := OpenVolume(write(t, "image.dmg", l.Bytes()))
			var u *UnsupportedChunkError
			if !errors.As(err, &u) || u.Type != c.typ || u.Name != c.name {
				t.Fatalf("err = %#v; want an *UnsupportedChunkError of type %#x, %s", err, c.typ, c.name)
			}
			if !strings.Contains(err.Error(), "hdiutil convert -format UDZO") {
				t.Fatalf("err = %v; want it to say how to convert the image", err)
			}
		})
	}
}

// sparse is a file of size bytes, all zero but its last ones, tail: an
// image claiming to be huge, without being so.
type sparse struct {
	size int64
	tail []byte
}

func (s sparse) ReadAt(p []byte, off int64) (int, error) {
	if off >= s.size {
		return 0, io.EOF
	}
	n := 0
	for ; n < len(p) && off+int64(n) < s.size; n++ {
		if i := off + int64(n) - (s.size - int64(len(s.tail))); i >= 0 {
			p[n] = s.tail[i]
		} else {
			p[n] = 0
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// openAndReadAll opens an image and reads its whole volume (up to 16
// MiB of it), giving the first error either step gives. It fails the test
// on a panic, and on a hang of over 20 seconds.
func openAndReadAll(t *testing.T, r io.ReaderAt, size int64) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("PANIC: %v", p)
			}
		}()
		v, err := openVolume(r, size)
		if err != nil {
			done <- err
			return
		}
		buf := make([]byte, 64<<10)
		for off := int64(0); off < min(v.Size, 16<<20); off += int64(len(buf)) {
			if _, err := v.ReadAt(buf, off); err != nil && err != io.EOF {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil && strings.HasPrefix(err.Error(), "PANIC") {
			t.Fatal(err)
		}
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("opening and reading the image hung")
		return nil
	}
}

func TestCorruptUDIFTablesAreRefused(t *testing.T) {
	disk := fixture.APM(testVolume())
	layout := func(c fixture.Chunk, edit func(l *fixture.Layout)) func() []byte {
		return func() []byte {
			l := fixture.UDIFLayout(disk, c, 7)
			edit(l)
			return l.Bytes()
		}
	}
	lastChunk := func(l *fixture.Layout) *fixture.ChunkEntry {
		b := &l.Blocks[len(l.Blocks)-1]
		return &b.Chunks[len(b.Chunks)-1]
	}
	image := func(c fixture.Chunk, edit func(img []byte) []byte) func() []byte {
		return func() []byte { return edit(fixture.UDIF(disk, c, 7)) }
	}
	koly := func(at int, v uint64) func(img []byte) []byte {
		return func(img []byte) []byte {
			binary.BigEndian.PutUint64(img[len(img)-512+at:], v)
			return img
		}
	}
	replace := func(old, new string) func(img []byte) []byte {
		return func(img []byte) []byte {
			if len(old) != len(new) || !bytes.Contains(img, []byte(old)) {
				panic("replace: " + old)
			}
			return bytes.Replace(img, []byte(old), []byte(new), 1)
		}
	}
	for _, c := range []struct {
		name string
		img  func() []byte
		want string
	}{
		{"a gap in the sector coverage", layout(fixture.Zlib, func(l *fixture.Layout) {
			b := &l.Blocks[1]
			b.Chunks = append(b.Chunks[:2], b.Chunks[3:]...)
		}), "uncovered"},
		{"chunks that overlap", layout(fixture.Zlib, func(l *fixture.Layout) {
			l.Blocks[1].Chunks[2].Sector--
		}), "overlap"},
		{"a trailer and a table of no sectors", layout(fixture.Zlib, func(l *fixture.Layout) {
			for i := range l.Blocks {
				l.Blocks[i].Chunks = nil
			}
			l.Sectors = 0
		}), "claims no sectors"},
		{"a trailer of no sectors", image(fixture.Zlib, koly(492, 0)), "claims no sectors"},
		{"too few sectors for the trailer", layout(fixture.Zlib, func(l *fixture.Layout) { l.Sectors++ }), "uncovered"},
		{"too many sectors for the trailer", layout(fixture.Zlib, func(l *fixture.Layout) { l.Sectors-- }), "past the trailer's"},
		{"a chunk past the data fork", layout(fixture.Zlib, func(l *fixture.Layout) {
			l.Blocks[1].Chunks[2].Offset = uint64(len(l.Data))
		}), "outside the"},
		{"a raw chunk of the wrong length", layout(fixture.Raw, func(l *fixture.Layout) {
			l.Blocks[1].Chunks[2].Length--
		}), "stores"},
		{"a chunk that decompresses short", layout(fixture.Zlib, func(l *fixture.Layout) {
			lastChunk(l).Sectors++
			l.Sectors++
		}), "decompresses to"},
		{"a chunk that decompresses long", layout(fixture.Bzip2, func(l *fixture.Layout) {
			c := lastChunk(l)
			more := fixture.Bzip2Compress(make([]byte, (c.Sectors+1)*512))
			c.Offset, c.Length = uint64(len(l.Data)), uint64(len(more))
			l.Data = append(l.Data, more...)
		}), "decompresses to more than"},
		{"a chunk over 64 MiB decompressed", layout(fixture.Zlib, func(l *fixture.Layout) {
			c := lastChunk(l)
			l.Sectors += 131073 - c.Sectors
			c.Sectors = 131073
		}), "64 MiB"},
		{"damaged zlib data", layout(fixture.Zlib, func(l *fixture.Layout) {
			c := lastChunk(l)
			for i := c.Offset + 2; i < c.Offset+c.Length; i++ {
				l.Data[i] ^= 0x5a
			}
		}), "zlib"},
		{"damaged bzip2 data", layout(fixture.Bzip2, func(l *fixture.Layout) {
			c := lastChunk(l)
			for i := c.Offset + 10; i < c.Offset+c.Length; i++ {
				l.Data[i] ^= 0x5a
			}
		}), "bzip2"},
		{"a block whose sectors overflow", layout(fixture.Zlib, func(l *fixture.Layout) {
			l.Blocks[1].Base = 1 << 62
		}), "sector"},
		{"the XML offset past the end", image(fixture.Zlib, func(img []byte) []byte {
			return koly(216, uint64(len(img)))(img)
		}), "runs past the end"},
		{"the XML length past the end", image(fixture.Zlib, koly(224, 1<<40)), "runs past the end"},
		{"no XML at all", image(fixture.Zlib, koly(224, 0)), "no XML plist"},
		{"the data fork past the end", image(fixture.Zlib, koly(32, 1<<40)), "data fork"},
		{"an absurd sector count", image(fixture.Zlib, koly(492, 1<<62)), "sectors"},
		{"no blkx table", image(fixture.Zlib, replace("<key>blkx</key>", "<key>blkz</key>")), "blkx"},
		{"bad base64", image(fixture.Zlib, replace("<data>\n\t\t\t\tbWlz", "<data>\n\t\t\t\t!Wlz")), "base64"},
		{"XML that is no XML", image(fixture.Zlib, replace("</plist>", "</plisx>")), "plist"},
		{"a truncated image", image(fixture.Zlib, func(img []byte) []byte { return img[:len(img)/2] }), "not a disc image"},
		{"a truncated APM disc", func() []byte { return disk[:len(disk)-4096] }, "runs past the end"},
	} {
		t.Run(c.name, func(t *testing.T) {
			img := c.img()
			err := openAndReadAll(t, bytes.NewReader(img), int64(len(img)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v; want one containing %q", err, c.want)
			}
			if c.want != "not a disc image" && !errors.Is(err, ErrDamaged) {
				t.Fatalf("err = %v; want it to be ErrDamaged", err)
			}
		})
	}
	t.Run("an XML plist too big to read", func(t *testing.T) {
		tail := make([]byte, 512)
		copy(tail, "koly")
		binary.BigEndian.PutUint64(tail[216:], 0)
		binary.BigEndian.PutUint64(tail[224:], 1<<30)
		binary.BigEndian.PutUint64(tail[492:], 8)
		err := openAndReadAll(t, sparse{size: 2 << 30, tail: tail}, 2<<30)
		if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), "more than") {
			t.Fatalf("err = %v; want ErrDamaged, saying the plist is more than this reader allows", err)
		}
	})
}

func TestDamagedPartitionTablesAreRefused(t *testing.T) {
	vol := hfsVolume(8192)
	for _, c := range []struct {
		name string
		img  func() []byte
		want string
	}{
		{"an APM block size of 0", func() []byte {
			img := fixture.APM(vol)
			img[2], img[3] = 0, 0
			return img
		}, "block size of 0"},
		{"an absurd APM block size", func() []byte {
			img := fixture.APM(vol)
			binary.BigEndian.PutUint16(img[2:], 777)
			return img
		}, "block size of 777"},
		{"an absurd APM entry count", func() []byte {
			img := fixture.APM(vol)
			for i := 1; i <= 3; i++ {
				binary.BigEndian.PutUint32(img[i*apmBlock+4:], 0xffffffff)
			}
			copy(img[3*apmBlock+48:], "Apple_Free\x00")
			return img
		}, "entries"},
		{"an APM entry past the end", func() []byte {
			img := fixture.APM(vol)
			binary.BigEndian.PutUint16(img[2:], 4096)
			return img[:5000]
		}, "past the end"},
		{"an APM partition past the end", func() []byte {
			img := fixture.APM(vol)
			binary.BigEndian.PutUint32(img[3*apmBlock+12:], 1<<30)
			return img
		}, "runs past the end"},
		{"a GPT header checksum", func() []byte {
			img := fixture.GPT(vol)
			img[512+40]++
			return img
		}, "header checksum"},
		{"a GPT entries checksum", func() []byte {
			img := fixture.GPT(vol)
			img[2*512+128+60]++
			return img
		}, "entries checksum"},
		{"a GPT partition past the end", func() []byte {
			img := fixture.GPT(vol)
			return img[:len(img)-50*512]
		}, "runs past the end"},
	} {
		t.Run(c.name, func(t *testing.T) {
			img := c.img()
			err := openAndReadAll(t, bytes.NewReader(img), int64(len(img)))
			if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v; want ErrDamaged, containing %q", err, c.want)
			}
		})
	}
	t.Run("a GPT with no Apple HFS partition", func(t *testing.T) {
		img := fixture.GPT(vol)
		copy(img[2*512+128:], fixture.GUID(fixture.EFISystemType))
		// Re-sum the entries and the header, so only the missing partition is wrong.
		fixGPTSums(img)
		_, _, _, err := hfsRange(img)
		if err == nil || !strings.Contains(err.Error(), "no Apple HFS partition") {
			t.Fatalf("err = %v; want one saying there is no Apple HFS partition", err)
		}
	})
}

func fixGPTSums(img []byte) {
	le := binary.LittleEndian
	h := img[512 : 512+92]
	entries := img[le.Uint64(h[72:])*512:][:le.Uint32(h[80:])*le.Uint32(h[84:])]
	le.PutUint32(h[88:], crc32.ChecksumIEEE(entries))
	clear(h[16:20])
	le.PutUint32(h[16:], crc32.ChecksumIEEE(h))
}

func TestParseMishRefusesShortTables(t *testing.T) {
	m := make([]byte, 204+40)
	copy(m, "mish")
	binary.BigEndian.PutUint32(m[200:], 2)
	for name, c := range map[string]struct {
		b    []byte
		want string
	}{
		"too short for a header": {m[:100], "bytes"},
		"no signature":           {append([]byte("hsim"), m[4:]...), "mish"},
		"more chunks than bytes": {m, "2 chunks"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseMish(c.b)
			if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v; want ErrDamaged, containing %q", err, c.want)
			}
		})
	}
}

func TestParsePlistRefusesDeepNesting(t *testing.T) {
	x := `<plist version="1.0">` + strings.Repeat("<array>", 100000) + strings.Repeat("</array>", 100000) + `</plist>`
	if _, err := parsePlist([]byte(x)); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Fatalf("err = %v; want one saying the plist nests too deep", err)
	}
}

// hfsTool is the path to the first of names on PATH, or skips the test,
// saying which tool is missing -- unless LONGBOAT_REQUIRE_HFS_TOOLS=1, as
// on CI's linux job, which fails it instead.
func hfsTool(t *testing.T, names ...string) string {
	t.Helper()
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	tool := strings.Join(names, " or ")
	if os.Getenv("LONGBOAT_REQUIRE_HFS_TOOLS") == "1" {
		t.Fatalf("%s is not on PATH, and LONGBOAT_REQUIRE_HFS_TOOLS=1 requires it", tool)
	}
	t.Skipf("%s is not on PATH: skipping (LONGBOAT_REQUIRE_HFS_TOOLS=1 requires it)", tool)
	return ""
}

// runTool runs name and gives its standard output.
func runTool(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s%s", name, strings.Join(args, " "), err, out, stderr.Bytes())
	}
	return out
}

// sevenZipDisk is the disk 7-Zip extracts from a UDIF image: one file per
// blkx entry, which in 7-Zip's listing order are the disk. The files'
// names differ between 7-Zip versions, so the listing gives the order,
// never the names.
func sevenZipDisk(t *testing.T, tool, image string) []byte {
	t.Helper()
	listing := string(runTool(t, tool, "l", "-slt", "-tdmg", image))
	_, items, ok := strings.Cut(listing, "\n----------\n")
	if !ok {
		t.Fatalf("7-Zip's listing has no item separator:\n%s", listing)
	}
	var paths []string
	for _, line := range strings.Split(items, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "Path = "); ok {
			paths = append(paths, p)
		}
	}
	dir := t.TempDir()
	runTool(t, tool, "x", "-tdmg", "-y", "-o"+dir, image)
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != len(paths) {
		t.Fatalf("7-Zip listed %d items, %q, but extracted %d (%v)", len(paths), paths, len(entries), err)
	}
	var disk []byte
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		disk = append(disk, b...)
	}
	return disk
}

func TestDmg2imgAndSevenZipAgreeWithTheUDIFFixture(t *testing.T) {
	vol := testVolume()
	disks := map[string][]byte{"APM": fixture.APM(vol), "GPT": fixture.GPT(vol)}
	for _, c := range []struct {
		disk  string
		chunk fixture.Chunk
	}{
		{"APM", fixture.Raw}, {"APM", fixture.Zero}, {"APM", fixture.Zlib}, {"APM", fixture.Bzip2}, {"GPT", fixture.Zlib},
	} {
		disk := disks[c.disk]
		image := write(t, "image.dmg", fixture.UDIF(disk, c.chunk, 7))
		t.Run("dmg2img/"+c.disk+"/"+c.chunk.String(), func(t *testing.T) {
			tool := hfsTool(t, "dmg2img")
			out := filepath.Join(t.TempDir(), "disk.img")
			runTool(t, tool, "-s", "-i", image, "-o", out)
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, disk) {
				t.Fatalf("dmg2img made %d bytes that are not the fixture's %d-byte disk", len(got), len(disk))
			}
		})
		t.Run("7z/"+c.disk+"/"+c.chunk.String(), func(t *testing.T) {
			if got := sevenZipDisk(t, hfsTool(t, "7z", "7zz"), image); !bytes.Equal(got, disk) {
				t.Fatalf("7-Zip extracted %d bytes that are not the fixture's %d-byte disk", len(got), len(disk))
			}
		})
	}
}

// FuzzOpenVolume holds that no image, however damaged, panics or hangs
// the reader. Its seeds, every container a fixture makes, run as a test.
func FuzzOpenVolume(f *testing.F) {
	vol := hfsVolume(8192)
	f.Add(vol)
	f.Add(fixture.APM(vol))
	f.Add(fixture.GPT(vol))
	for _, c := range []fixture.Chunk{fixture.Raw, fixture.Zero, fixture.Zlib, fixture.Bzip2, fixture.LZFSE} {
		f.Add(fixture.UDIF(fixture.APM(vol), c, 3))
	}
	f.Fuzz(func(t *testing.T, img []byte) {
		v, err := openVolume(bytes.NewReader(img), int64(len(img)))
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		for off := int64(0); off < min(v.Size, 1<<20); off += int64(len(buf)) {
			if _, err := v.ReadAt(buf, off); err != nil && err != io.EOF {
				return
			}
		}
	})
}
