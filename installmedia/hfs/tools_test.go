package hfs

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mavergreen/longboat/installmedia/internal/fixture"
)

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

// runTool runs name and gives its standard output and error, together.
func runTool(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	out, err := runWithin(2*time.Minute, name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func runWithin(d time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// hdiutilTimeout bounds each hdiutil call, so a wedged attach or detach
// fails the test rather than hanging it.
const hdiutilTimeout = 30 * time.Second

// device is where the HFS tools can work on the image file at path.
// hfsprogs' fsck.hfsplus and mkfs.hfsplus, on Linux, take the file
// itself. Apple's fsck_hfs and newfs_hfs take only a device: given a
// file, fsck_hfs exits 8 with "Can't get device block size", and
// newfs_hfs looks for /dev/r<file>. So on macOS the file is attached,
// unprivileged and unmounted, as a raw disk, and its /dev/diskN given
// instead. detach detaches it, once; t.Cleanup calls it too, so the
// device is detached on every path a test leaves by, short of the test
// binary being killed outright.
func device(t *testing.T, path string) (dev string, detach func()) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return path, func() {}
	}
	hdiutil := hfsTool(t, "hdiutil")
	args := []string{"attach", "-nomount", "-noverify", "-imagekey", "diskimage-class=CRawDiskImage", path}
	out, err := runWithin(hdiutilTimeout, hdiutil, args...)
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], "/dev/disk") {
			dev = f[0]
			break
		}
	}
	if dev != "" {
		var once sync.Once
		detach = func() {
			once.Do(func() {
				if out, err := detachDevice(hdiutil, dev); err != nil {
					t.Errorf("hdiutil detach %s: %v\n%s", dev, err, out)
				}
			})
		}
		t.Cleanup(detach)
	}
	if err != nil || dev == "" {
		t.Fatalf("hdiutil %s: %v, giving no /dev/disk:\n%s", strings.Join(args, " "), err, out)
	}
	return dev, detach
}

// detachDevice detaches dev, forcing it if a plain detach fails.
func detachDevice(hdiutil, dev string) ([]byte, error) {
	if out, err := runWithin(hdiutilTimeout, hdiutil, "detach", dev); err == nil {
		return out, nil
	}
	return runWithin(hdiutilTimeout, hdiutil, "detach", "-force", dev)
}

func writeImage(t *testing.T, img []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "volume.img")
	if err := os.WriteFile(p, img, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// manyFiles is a catalog several levels deep in 512-byte nodes, with two
// files and the catalog itself fragmented past their records' 8 extents.
func manyFiles() ([]fixture.File, fixture.HFSOptions) {
	var files []fixture.File
	for i := range 400 {
		files = append(files, fixture.File{Path: fmt.Sprintf("Packages/file%03d.pkg", i), Data: noise(uint64(i), i*37%2000)})
	}
	files = append(files,
		fixture.File{Path: "Packages/Big.pkg", Data: noise(500, 21*512+9)},
		fixture.File{Path: "Packages/Other.pkg", Data: noise(501, 11*512)},
		fixture.File{Path: "System/Library/CoreServices/SystemVersion.plist", Data: []byte("<plist/>")},
		fixture.File{Path: "Empty/"},
	)
	return files, fixture.HFSOptions{
		BlockSize: 512, NodeSize: 512,
		Fragment: []string{"Packages/Big.pkg", "Packages/Other.pkg", fixture.CatalogFile},
	}
}

func TestSevenZipExtractsTheFixture(t *testing.T) {
	tool := hfsTool(t, "7z", "7zz")
	files, o := manyFiles()
	// 7-Zip 23.01 refuses a fork whose overflow extent records run on
	// from one leaf node of the extents B-tree into the next (MEASURED
	// 2026-10-14: "Headers Error", where fsck.hfsplus accepts the volume
	// and this reader reads it), so its fixture keeps them in one leaf:
	// 4 KiB nodes hold every overflow record of the 400-file catalog.
	o.NodeSize = 4096
	for name, o := range map[string]fixture.HFSOptions{"fragmented": o, "HFSX": {HFSX: true}} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			runTool(t, tool, "x", "-thfs", "-y", "-o"+dir, writeImage(t, fixture.HFS(files, o)))
			want := map[string][]byte{}
			for _, f := range files {
				if !strings.HasSuffix(f.Path, "/") {
					want[f.Path] = f.Data
				}
			}
			got := map[string][]byte{}
			// 7-Zip puts the volume's files in a directory named for it.
			err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(dir, p)
				_, rel, _ = strings.Cut(filepath.ToSlash(rel), "/")
				b, err := os.ReadFile(p)
				got[rel] = b
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("7-Zip extracted %d files; want %d:\n%v", len(got), len(want), keys(got))
			}
			for p, b := range want {
				if !bytes.Equal(got[p], b) {
					t.Errorf("%s: 7-Zip extracted %d bytes that are not its %d", p, len(got[p]), len(b))
				}
			}
			if fi, err := os.Stat(filepath.Join(dir, fixture.VolumeName, "Empty")); err != nil || !fi.IsDir() {
				t.Errorf("7-Zip made no empty directory Empty: %v", err)
			}
		})
	}
}

func keys(m map[string][]byte) []string {
	var k []string
	for p := range m {
		k = append(k, p)
	}
	sort.Strings(k)
	return k
}

func TestFsckAcceptsTheFixture(t *testing.T) {
	tool := hfsTool(t, "fsck.hfsplus", "fsck_hfs")
	files, o := manyFiles()
	small := treeFiles()
	for name, c := range map[string]struct {
		files []fixture.File
		o     fixture.HFSOptions
	}{
		"400 files, fragmented":     {files, o},
		"4 KiB blocks and nodes":    {small, fixture.HFSOptions{}},
		"8 KiB nodes, 2 KiB blocks": {small, fixture.HFSOptions{BlockSize: 2048, NodeSize: 8192}},
		"HFSX":                      {append(small, fixture.File{Path: "readme.txt", Data: []byte("lower")}), fixture.HFSOptions{HFSX: true}},
	} {
		t.Run(name, func(t *testing.T) {
			dev, _ := device(t, writeImage(t, fixture.HFS(c.files, c.o)))
			out := runTool(t, tool, "-n", dev)
			if !bytes.Contains(out, []byte("appears to be OK")) {
				t.Fatalf("fsck exited 0 but did not say the volume appears to be OK:\n%s", out)
			}
		})
	}
}

func TestFsckAcceptsAResourceForksOverflowRecords(t *testing.T) {
	tool := hfsTool(t, "fsck.hfsplus", "fsck_hfs")
	img, _, _ := resourceForkVolume(t)
	dev, _ := device(t, writeImage(t, img))
	if out := runTool(t, tool, "-n", dev); !bytes.Contains(out, []byte("appears to be OK")) {
		t.Fatalf("fsck exited 0 but did not say the volume appears to be OK:\n%s", out)
	}
}

// metadata are the names HFS+ itself keeps in a volume's root: its
// private directories and a journal's files.
var metadata = map[string]bool{
	"\x00\x00\x00\x00HFS+ Private Data": true,
	".HFS+ Private Directory Data\r":    true,
	".journal":                          true,
	".journal_info_block":               true,
}

// mountMetadata are the names macOS adds to a volume's root if it mounts
// it. The image is attached unmounted, but should Disk Arbitration
// mount the new volume newfs_hfs writes before it is detached, these
// are what that leaves, and still no user file.
var mountMetadata = map[string]bool{
	".fseventsd":      true,
	".Spotlight-V100": true,
	".Trashes":        true,
	".TemporaryItems": true,
}

func TestReadsAnEmptyVolumeMkfsMade(t *testing.T) {
	tool := hfsTool(t, "mkfs.hfsplus", "newfs_hfs")
	for name, args := range map[string][]string{
		"HFS+":      nil,
		"HFSX":      {"-s"},
		"journaled": {"-J"},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "empty.img")
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(p, 16<<20); err != nil {
				t.Fatal(err)
			}
			dev, detach := device(t, p)
			runTool(t, tool, append(append(args, "-v", "Empty"), dev)...)
			detach() // so the file holds all newfs_hfs wrote
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			v, err := Open(f, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			if v.hfsx != (name == "HFSX") {
				t.Fatalf("read as HFSX: %v", v.hfsx)
			}
			es, err := v.ReadDir("")
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range es {
				if !metadata[e.Name] && !(runtime.GOOS == "darwin" && mountMetadata[e.Name]) {
					t.Errorf("the empty volume's root names %q, which is no HFS+ metadata", e.Name)
				}
			}
			if name == "journaled" && len(es) == 0 {
				t.Error("the journaled volume's root names no journal files: the walk missed them")
			}
		})
	}
}
