package hfs

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// Catalog record types (TN1150).
const (
	folderRecord       = 1
	fileRecord         = 2
	folderThreadRecord = 3
	fileThreadRecord   = 4
)

// File modes' type bits, as BSD keeps them in a record's permissions.
const (
	modeType    = 0o170000
	modeRegular = 0o100000
	modeSymlink = 0o120000
)

// child is a folder or file record in a directory: its name as the
// catalog holds it, its entry and its record's body.
type child struct {
	name  []uint16
	entry Entry
	rec   []byte
}

// root is the root folder: the folder record, keyed under parent 1, whose
// CNID is 2.
func (v *Volume) root() (child, error) {
	kids, err := v.children(rootParentCNID)
	if err != nil {
		return child{}, err
	}
	for _, k := range kids {
		if k.entry.Kind == Dir && k.entry.CNID == rootCNID {
			return k, nil
		}
	}
	return child{}, corrupt("the catalog has no root folder (CNID %d, under parent %d)", rootCNID, rootParentCNID)
}

// children are the folder and file records under parent, in the
// catalog's order. Keys are (parent CNID, name), so the records under
// parent are contiguous; they are found by the CNID alone, a number,
// and their names never compared.
func (v *Volume) children(parent uint32) ([]child, error) {
	var kids []child
	err := v.catalog.walk(
		func(key []byte) (bool, error) { return binary.BigEndian.Uint32(key) < parent, nil },
		func(key, rec []byte) (bool, error) {
			switch p := binary.BigEndian.Uint32(key); {
			case p < parent:
				return false, nil
			case p > parent:
				return true, nil
			}
			c, ok, err := v.record(key, rec)
			if ok {
				kids = append(kids, c)
			}
			return false, err
		})
	return kids, err
}

// record is the child that the leaf record key and rec describe, or not
// ok for a thread record, which names its own CNID's parent.
func (v *Volume) record(key, rec []byte) (c child, ok bool, err error) {
	nl := int(binary.BigEndian.Uint16(key[4:]))
	if nl > 255 || 6+2*nl > len(key) {
		return child{}, false, corrupt("a catalog key's name length, %d, runs past its %d-byte key", nl, len(key))
	}
	c.name = make([]uint16, nl)
	for i := range c.name {
		c.name[i] = binary.BigEndian.Uint16(key[6+2*i:])
	}
	name := string(utf16.Decode(c.name))
	if len(rec) < 2 {
		return child{}, false, corrupt("the catalog record for %q is %d bytes, too short for a record type", name, len(rec))
	}
	short := func(kind string, want int) error {
		return corrupt("the catalog's %s record for %q is %d bytes, shorter than %d", kind, name, len(rec), want)
	}
	switch typ := binary.BigEndian.Uint16(rec); typ {
	case folderThreadRecord, fileThreadRecord:
		return child{}, false, nil
	case folderRecord:
		if len(rec) < folderRecordLen {
			return child{}, false, short("folder", folderRecordLen)
		}
		c.entry = Entry{Name: name, CNID: binary.BigEndian.Uint32(rec[8:]), Kind: Dir}
	case fileRecord:
		if len(rec) < fileRecordLen {
			return child{}, false, short("file", fileRecordLen)
		}
		size, err := v.checkForkSize(rec[fileDataForkAt:], fmt.Sprintf("the data fork of %q", name))
		if err != nil {
			return child{}, false, err
		}
		c.entry = Entry{
			Name:       name,
			CNID:       binary.BigEndian.Uint32(rec[8:]),
			Kind:       fileKind(rec),
			Size:       size,
			Compressed: rec[ownerFlagsAt]&ufCompressed != 0,
		}
	default:
		return child{}, false, corrupt("the catalog has a record of type %d for %q, which HFS+ does not define", typ, name)
	}
	c.rec = rec
	return c, true, nil
}

// fileKind is what a file record is, from its Finder type and creator
// and its BSD mode. A mode of 0 is a file never given one: a regular
// file.
func fileKind(rec []byte) Kind {
	switch string(rec[fileTypeAt : fileTypeAt+8]) {
	case "hlnkhfs+", "fdrpMACS":
		return HardLink
	case "slnkrhap":
		return Symlink
	}
	switch binary.BigEndian.Uint16(rec[fileModeAt:]) & modeType {
	case 0, modeRegular:
		return File
	case modeSymlink:
		return Symlink
	}
	return Other
}
