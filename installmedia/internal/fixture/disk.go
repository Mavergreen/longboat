// Package fixture builds disc images at test time, so the module never
// carries Apple's bytes: partition maps, GUID partition tables and UDIF
// images around whatever volume bytes a test supplies.
package fixture

import (
	"encoding/binary"
	"hash/crc32"
	"unicode/utf16"
)

// apmBlock is the block size of a retail disc's partition map: the 10.6
// retail DVD's driver descriptor says 2048 (MEASURED 2026-10-04).
const apmBlock = 2048

// APM lays volume out as the 10.6 retail DVD is: a driver descriptor
// with 2048-byte blocks, a three-entry map, a Boot Camp ATAPI driver
// region, then the Apple_HFS partition holding volume, padded with zeros
// to whole blocks.
func APM(volume []byte) []byte {
	hfsBlocks := (len(volume) + apmBlock - 1) / apmBlock
	img := make([]byte, (6+hfsBlocks)*apmBlock)
	copy(img, "ER")
	binary.BigEndian.PutUint16(img[2:], apmBlock)
	entries := []struct {
		start, count uint32
		name, typ    string
	}{
		{1, 3, "Apple", "Apple_partition_map"},
		{4, 2, "Macintosh", "Apple_Driver_ATAPI"},
		{6, uint32(hfsBlocks), "Mac_OS_X", "Apple_HFS"},
	}
	for i, e := range entries {
		b := img[(1+i)*apmBlock:]
		copy(b, "PM")
		binary.BigEndian.PutUint32(b[4:], uint32(len(entries)))
		binary.BigEndian.PutUint32(b[8:], e.start)
		binary.BigEndian.PutUint32(b[12:], e.count)
		copy(b[16:48], e.name)
		copy(b[48:80], e.typ)
	}
	copy(img[6*apmBlock:], volume)
	return img
}

const (
	sector     = 512
	gptEntries = 128
	gptEntry   = 128
	// gptTableSectors is the 16 KiB of entries, in sectors.
	gptTableSectors = gptEntries * gptEntry / sector
)

// AppleHFSType and EFISystemType are GPT partition type GUIDs.
const (
	AppleHFSType  = "48465300-0000-11AA-AA11-00306543ECAC"
	EFISystemType = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
)

// GUID is a GUID in GPT's on-disk order: its first three fields
// little-endian, the rest as written.
func GUID(s string) []byte {
	hex := func(c byte) byte {
		switch {
		case c >= '0' && c <= '9':
			return c - '0'
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10
		}
		panic("fixture.GUID: not hex: " + s)
	}
	var raw []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			continue
		}
		raw = append(raw, hex(s[i])<<4|hex(s[i+1]))
		i++
	}
	if len(raw) != 16 {
		panic("fixture.GUID: not a GUID: " + s)
	}
	b := append([]byte(nil), raw...)
	b[0], b[1], b[2], b[3] = raw[3], raw[2], raw[1], raw[0]
	b[4], b[5] = raw[5], raw[4]
	b[6], b[7] = raw[7], raw[6]
	return b
}

// GPT lays volume out as hdiutil's GUID partition scheme does, with
// 512-byte sectors: a protective MBR, the primary header and entries, a
// small EFI system partition, then one Apple HFS partition holding volume
// (padded to whole sectors), and the backup entries and header at the end.
func GPT(volume []byte) []byte {
	const efiStart, efiSectors = 40, 8
	hfsStart := uint64(efiStart + efiSectors)
	hfsSectors := uint64((len(volume) + sector - 1) / sector)
	total := hfsStart + hfsSectors + 1 + gptTableSectors
	img := make([]byte, total*sector)

	// The protective MBR: one partition of type 0xEE over the whole disk.
	mbr := img[446:]
	mbr[4] = 0xee
	binary.LittleEndian.PutUint32(mbr[8:], 1)
	binary.LittleEndian.PutUint32(mbr[12:], uint32(min(total-1, 0xffffffff)))
	img[510], img[511] = 0x55, 0xaa

	table := make([]byte, gptEntries*gptEntry)
	entry := func(i int, typ, id string, first, last uint64, name string) {
		e := table[i*gptEntry:]
		copy(e, GUID(typ))
		copy(e[16:], GUID(id))
		binary.LittleEndian.PutUint64(e[32:], first)
		binary.LittleEndian.PutUint64(e[40:], last)
		for j, u := range utf16.Encode([]rune(name)) {
			binary.LittleEndian.PutUint16(e[56+2*j:], u)
		}
	}
	entry(0, EFISystemType, "8A1B6C2D-0001-4E5F-8A9B-0C1D2E3F4A5B", efiStart, efiStart+efiSectors-1, "EFI System Partition")
	entry(1, AppleHFSType, "8A1B6C2D-0002-4E5F-8A9B-0C1D2E3F4A5B", hfsStart, hfsStart+hfsSectors-1, "Mac OS X Install DVD")
	tableSum := crc32.ChecksumIEEE(table)

	header := func(self, backup, entriesAt uint64) {
		h := img[self*sector:][:92]
		copy(h, "EFI PART")
		binary.LittleEndian.PutUint32(h[8:], 0x00010000)
		binary.LittleEndian.PutUint32(h[12:], 92)
		binary.LittleEndian.PutUint64(h[24:], self)
		binary.LittleEndian.PutUint64(h[32:], backup)
		binary.LittleEndian.PutUint64(h[40:], 2+gptTableSectors)
		binary.LittleEndian.PutUint64(h[48:], total-2-gptTableSectors)
		copy(h[56:], GUID("8A1B6C2D-0000-4E5F-8A9B-0C1D2E3F4A5B"))
		binary.LittleEndian.PutUint64(h[72:], entriesAt)
		binary.LittleEndian.PutUint32(h[80:], gptEntries)
		binary.LittleEndian.PutUint32(h[84:], gptEntry)
		binary.LittleEndian.PutUint32(h[88:], tableSum)
		binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h))
		copy(img[entriesAt*sector:], table)
	}
	header(1, total-1, 2)
	header(total-1, 1, total-1-gptTableSectors)
	copy(img[hfsStart*sector:], volume)
	return img
}
