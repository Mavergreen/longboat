package hfs

import (
	"cmp"
	"encoding/binary"
	"io"
	"sort"
)

type extent struct{ start, count uint32 }

// fork is a file's data fork, found block by block: an io.ReaderAt of
// its size bytes.
type fork struct {
	r         io.ReaderAt
	blockSize int64
	size      int64
	extents   []extent
	ends      []int64 // the fork offset each extent ends at
}

// newFork reads the HFSPlusForkData at fd: its size, its first 8
// extents, and, for a fork needing more, the overflow extent records of
// cnid. Every extent must lie inside the volume.
func (v *Volume) newFork(cnid uint32, fd []byte, what string) (*fork, error) {
	size, err := v.checkForkSize(fd, what)
	if err != nil {
		return nil, err
	}
	need := binary.BigEndian.Uint32(fd[12:])
	f := &fork{r: v.r, blockSize: v.blockSize, size: size}
	have := uint32(0)
	add := func(rec []byte) (uint32, error) {
		added := uint32(0)
		for i := 0; i < 8; i++ {
			e := extent{binary.BigEndian.Uint32(rec[8*i:]), binary.BigEndian.Uint32(rec[8*i+4:])}
			if e.count == 0 {
				break
			}
			if uint64(e.start)+uint64(e.count) > uint64(v.blocks) {
				return 0, corrupt("%s has an extent of %d blocks at block %d, past the end of the volume's %d blocks", what, e.count, e.start, v.blocks)
			}
			if uint64(have)+uint64(added)+uint64(e.count) > uint64(v.blocks) {
				return 0, corrupt("%s has extents of more blocks than the volume's %d", what, v.blocks)
			}
			f.extents = append(f.extents, e)
			added += e.count
			f.ends = append(f.ends, int64(have+added)*v.blockSize)
		}
		return added, nil
	}
	added, err := add(fd[16:])
	if err != nil {
		return nil, err
	}
	have += added
	if have >= need {
		return f, nil
	}
	if cnid == extentsCNID {
		return nil, corrupt("%s needs more than the 8 extents its fork data holds, and cannot have overflow extents of its own", what)
	}
	// One ordered walk: from the first key at or after (cnid, data fork,
	// block 0), through every record of this fork, each starting where
	// the extents so far end.
	err = v.extents.walk(
		func(key []byte) (bool, error) { return compareExtentKey(key, cnid, dataFork, 0) < 0, nil },
		func(key, rec []byte) (bool, error) {
			if compareExtentKey(key, cnid, dataFork, 0) < 0 {
				return false, nil
			}
			if binary.BigEndian.Uint32(key[2:]) != cnid || key[0] != dataFork {
				return true, nil // past this fork's records
			}
			if start := binary.BigEndian.Uint32(key[6:]); start != have {
				return true, corrupt("%s has an overflow extent record starting at block %d of the fork, where its extents so far end at block %d", what, start, have)
			}
			if len(rec) < 64 {
				return true, corrupt("%s has an overflow extent record, for its blocks from %d, of %d bytes, shorter than 64", what, have, len(rec))
			}
			added, err := add(rec)
			if err != nil {
				return true, err
			}
			if added == 0 {
				return true, corrupt("%s has an overflow extent record, for its blocks from %d, that adds no blocks", what, have)
			}
			have += added
			return have >= need, nil
		})
	if err != nil {
		return nil, err
	}
	if have < need {
		return nil, corrupt("%s needs an overflow extent record for its blocks from %d, and the extents overflow file has none", what, have)
	}
	return f, nil
}

// ReadAt reads the fork's bytes at off.
func (f *fork) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, io.ErrUnexpectedEOF
	}
	if off >= f.size {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > f.size-off {
		p = p[:f.size-off]
	}
	n := 0
	for len(p) > 0 {
		i := sort.Search(len(f.ends), func(i int) bool { return f.ends[i] > off })
		e := f.extents[i]
		begin := f.ends[i] - int64(e.count)*f.blockSize
		chunk := int(min(int64(len(p)), f.ends[i]-off))
		m, err := f.r.ReadAt(p[:chunk], int64(e.start)*f.blockSize+off-begin)
		n += m
		if m < chunk {
			if err == nil || err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return n, err
		}
		p, off = p[chunk:], off+int64(chunk)
	}
	if n < want {
		return n, io.EOF
	}
	return n, nil
}

// dataFork is the fork type of a data fork in an extent key; a resource
// fork's is 0xFF.
const dataFork = 0x00

// compareExtentKey orders an extents overflow key, without its length
// field, against (cnid, forkType, start) as TN1150 and Apple's
// CompareExtentKeysPlus do: by file ID, then fork type, then start
// block. On disk the fork type comes first (byte 0, then a pad byte),
// the file ID at 2 and the start block at 6.
func compareExtentKey(key []byte, cnid uint32, forkType byte, start uint32) int {
	if c := cmp.Compare(binary.BigEndian.Uint32(key[2:]), cnid); c != 0 {
		return c
	}
	if c := cmp.Compare(key[0], forkType); c != 0 {
		return c
	}
	return cmp.Compare(binary.BigEndian.Uint32(key[6:]), start)
}
