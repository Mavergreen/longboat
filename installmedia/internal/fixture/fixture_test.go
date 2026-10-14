package fixture

import (
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"testing"
)

func TestBzip2RoundTripsThroughTheStandardLibrary(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	random := make([]byte, 50000)
	for i := range random {
		random[i] = byte(rng.Uint32())
	}
	runs := bytes.Repeat([]byte("aaaaaaaaaabbbbcccccccccccccccccccccccccccccccccccccccccccccccc"), 300)
	every := make([]byte, 256*3)
	for i := range every {
		every[i] = byte(i)
	}
	big := make([]byte, 1500000) // more than one 900k block
	for i := range big {
		if i%4096 < 2048 {
			big[i] = byte(rng.Uint32())
		}
	}
	for name, in := range map[string][]byte{
		"empty":          nil,
		"one byte":       {'x'},
		"two bytes":      {'x', 'y'},
		"four of a kind": {7, 7, 7, 7},
		"five of a kind": {7, 7, 7, 7, 7},
		"long zeros":     make([]byte, 100000),
		"255 then 4":     bytes.Repeat([]byte{9}, 259),
		"periodic":       bytes.Repeat([]byte("ab"), 1000),
		"random":         random,
		"runs":           runs,
		"every byte":     every,
		"two blocks":     big,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(Bzip2Compress(in))))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, in) {
				t.Fatalf("round trip gave %d bytes, want %d equal ones", len(got), len(in))
			}
		})
	}
}

func volume(n int) []byte {
	v := make([]byte, n)
	copy(v[1024:], "H+\x00\x04")
	copy(v[n-8:], "THE-END!")
	return v
}

func TestAPMIsLaidOutAsARetailDVD(t *testing.T) {
	vol := volume(3 * 2048)
	img := APM(vol)
	if string(img[:2]) != "ER" || binary.BigEndian.Uint16(img[2:]) != 2048 {
		t.Fatalf("driver descriptor = %q", img[:4])
	}
	if !bytes.Equal(img[6*2048:], vol) {
		t.Fatal("the Apple_HFS partition at block 6 does not hold the volume")
	}
	e := img[3*2048:]
	if string(e[:2]) != "PM" || string(bytes.TrimRight(e[48:80], "\x00")) != "Apple_HFS" ||
		binary.BigEndian.Uint32(e[8:]) != 6 || binary.BigEndian.Uint32(e[12:]) != 3 {
		t.Fatalf("entry 3 = %q", e[:80])
	}
	if got := len(APM(volume(2049))); got != 8*2048 {
		t.Fatalf("a volume of 2049 bytes made a %d-byte disc; want it padded to whole blocks, %d", got, 8*2048)
	}
}

func TestGPTHasOneAppleHFSPartitionAndValidChecksums(t *testing.T) {
	vol := volume(8 * 512)
	img := GPT(vol)
	if len(img)%512 != 0 || img[510] != 0x55 || img[511] != 0xaa || img[450] != 0xee {
		t.Fatal("no protective MBR")
	}
	for _, lba := range []int64{1, int64(len(img)/512 - 1)} {
		h := append([]byte(nil), img[lba*512:lba*512+92]...)
		if string(h[:8]) != "EFI PART" {
			t.Fatalf("LBA %d: no GPT header", lba)
		}
		sum := binary.LittleEndian.Uint32(h[16:])
		clear(h[16:20])
		if crc32.ChecksumIEEE(h) != sum {
			t.Fatalf("LBA %d: header checksum is wrong", lba)
		}
		entries := img[binary.LittleEndian.Uint64(h[72:])*512:][:128*128]
		if crc32.ChecksumIEEE(entries) != binary.LittleEndian.Uint32(h[88:]) {
			t.Fatalf("LBA %d: entries checksum is wrong", lba)
		}
	}
	e := img[2*512+128:]
	hfs := []byte{0x00, 0x53, 0x46, 0x48, 0x00, 0x00, 0xaa, 0x11, 0xaa, 0x11, 0x00, 0x30, 0x65, 0x43, 0xec, 0xac}
	if !bytes.Equal(e[:16], hfs) {
		t.Fatalf("entry 1's type = %x, want Apple HFS's", e[:16])
	}
	first, last := binary.LittleEndian.Uint64(e[32:]), binary.LittleEndian.Uint64(e[40:])
	if !bytes.Equal(img[first*512:(last+1)*512], vol) {
		t.Fatal("the Apple HFS partition does not hold the volume")
	}
}

func TestUDIFStoresEveryChunkKind(t *testing.T) {
	disk := make([]byte, 40*512)
	for i := 512; i < 20*512; i++ {
		disk[i] = byte(i * 7)
	}
	for _, c := range []Chunk{Raw, Zero, Zlib, Bzip2, LZFSE} {
		l := UDIFLayout(disk, c, 7)
		var sectors uint64
		types := map[uint32]bool{}
		for _, b := range l.Blocks {
			if b.Base != sectors {
				t.Fatalf("%v: block base %d, want %d", c, b.Base, sectors)
			}
			for _, ch := range b.Chunks {
				types[ch.Type] = true
				if ch.Type&0x7fffffff == 0x7ffffffe {
					continue
				}
				if b.Base+ch.Sector != sectors {
					t.Fatalf("%v: chunk at %d, want %d", c, b.Base+ch.Sector, sectors)
				}
				sectors += ch.Sectors
				stored := l.Data[ch.Offset : ch.Offset+ch.Length]
				want := disk[(b.Base+ch.Sector)*512 : (b.Base+ch.Sector+ch.Sectors)*512]
				var got []byte
				switch ch.Type {
				case 0x1, 0x80000007:
					got = stored
				case 0x0, 0x2:
					got = make([]byte, len(want))
				case 0x80000005:
					zr, err := zlib.NewReader(bytes.NewReader(stored))
					if err != nil {
						t.Fatal(err)
					}
					got, _ = io.ReadAll(zr)
				case 0x80000006:
					got, _ = io.ReadAll(bzip2.NewReader(bytes.NewReader(stored)))
				default:
					t.Fatalf("%v: chunk type %#x", c, ch.Type)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("%v: chunk at sector %d holds the wrong bytes", c, b.Base+ch.Sector)
				}
			}
		}
		if sectors != l.Sectors || l.Sectors != 40 {
			t.Fatalf("%v: chunks cover %d sectors, the layout says %d; want 40", c, sectors, l.Sectors)
		}
		if c == Zero && !(types[0x0] && types[0x2] && types[0x1]) {
			t.Fatalf("Zero made chunk types %v; want 0x0, 0x2 and raw", types)
		}
		img := l.Bytes()
		koly := img[len(img)-512:]
		if string(koly[:4]) != "koly" || binary.BigEndian.Uint64(koly[492:]) != 40 ||
			binary.BigEndian.Uint64(koly[32:]) != uint64(len(l.Data)) {
			t.Fatalf("%v: koly trailer = %x", c, koly[:64])
		}
		xml := img[binary.BigEndian.Uint64(koly[216:]):][:binary.BigEndian.Uint64(koly[224:])]
		if !bytes.Contains(xml, []byte("<key>blkx</key>")) {
			t.Fatalf("%v: the XML has no blkx table", c)
		}
		if !bytes.Equal(UDIF(disk, c, 7), img) {
			t.Fatalf("%v: UDIF and UDIFLayout(...).Bytes() differ", c)
		}
	}
}
