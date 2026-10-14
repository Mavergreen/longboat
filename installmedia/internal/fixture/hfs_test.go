package fixture

import (
	"encoding/binary"
	"testing"
)

// folder is what a folder record says of itself.
type folder struct {
	flags                uint16
	valence, folderCount uint32
}

// folders decodes every folder record in the catalog of img, a volume
// HFS made with its catalog contiguous, by CNID.
func folders(t *testing.T, img []byte) map[uint32]folder {
	t.Helper()
	h := img[1024:]
	bs := int(binary.BigEndian.Uint32(h[40:]))
	cat := img[int(binary.BigEndian.Uint32(h[272+16:]))*bs:]
	ns := int(binary.BigEndian.Uint16(cat[14+18:]))
	out := map[uint32]folder{}
	for n := binary.BigEndian.Uint32(cat[14+10:]); n != 0; {
		node := cat[int(n)*ns:][:ns]
		for i := range int(binary.BigEndian.Uint16(node[10:])) {
			rec := node[binary.BigEndian.Uint16(node[ns-2*(i+1):]):]
			body := rec[2+int(binary.BigEndian.Uint16(rec)):]
			if binary.BigEndian.Uint16(body) == hfsFolder {
				out[binary.BigEndian.Uint32(body[8:])] = folder{
					flags:       binary.BigEndian.Uint16(body[2:]),
					valence:     binary.BigEndian.Uint32(body[4:]),
					folderCount: binary.BigEndian.Uint32(body[84:]),
				}
			}
		}
		n = binary.BigEndian.Uint32(node)
	}
	return out
}

// HFSX folder records keep a count of their child folders, and say so
// with kHFSHasFolderCountMask: Apple's fsck_hfs requires both on a
// case-sensitive volume ("HasFolderCount flag needs to be set"). An
// HFS+ volume's folders, which xnu never counts, leave both zero, as
// Apple's fsck_hfs accepts.
func TestHFSXFoldersCountTheirChildFolders(t *testing.T) {
	files := []File{
		{Path: "A/one", Data: []byte("1")},
		{Path: "A/sub/two", Data: []byte("2")},
		{Path: "B/"},
		{Path: "C/D/E/"},
		{Path: "top", Data: []byte("t")},
	}
	// The root holds A, B, C and top; A holds one and sub; C holds D.
	want := map[string]folder{
		"root": {0x10, 4, 3}, "A": {0x10, 2, 1}, "sub": {0x10, 1, 0},
		"B": {0x10, 0, 0}, "C": {0x10, 1, 1}, "D": {0x10, 1, 1}, "E": {0x10, 0, 0},
	}
	// CNIDs are given from 16, a directory's children in name order and
	// then each child's: A 16, B 17, C 18, top 19, one 20, sub 21, two 22,
	// D 23, E 24.
	cnids := map[string]uint32{"root": 2, "A": 16, "B": 17, "C": 18, "sub": 21, "D": 23, "E": 24}
	got := folders(t, HFS(files, HFSOptions{HFSX: true}))
	if len(got) != len(want) {
		t.Fatalf("decoded %d folder records; want %d: %+v", len(got), len(want), got)
	}
	for name, w := range want {
		if g := got[cnids[name]]; g != w {
			t.Errorf("HFSX folder %s (CNID %d) = %+v; want %+v", name, cnids[name], g, w)
		}
	}
	for cnid, g := range folders(t, HFS(files, HFSOptions{})) {
		if g.flags != 0 || g.folderCount != 0 {
			t.Errorf("HFS+ folder CNID %d = %+v; want no folder count", cnid, g)
		}
	}
}
