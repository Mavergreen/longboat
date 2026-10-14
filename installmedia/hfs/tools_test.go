package hfs

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
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
			out := runTool(t, tool, "-n", writeImage(t, fixture.HFS(c.files, c.o)))
			if !bytes.Contains(out, []byte("appears to be OK")) {
				t.Fatalf("fsck exited 0 but did not say the volume appears to be OK:\n%s", out)
			}
		})
	}
}

func TestFsckAcceptsAResourceForksOverflowRecords(t *testing.T) {
	tool := hfsTool(t, "fsck.hfsplus", "fsck_hfs")
	img, _, _ := resourceForkVolume(t)
	if out := runTool(t, tool, "-n", writeImage(t, img)); !bytes.Contains(out, []byte("appears to be OK")) {
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
			runTool(t, tool, append(append(args, "-v", "Empty"), p)...)
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
				if !metadata[e.Name] {
					t.Errorf("the empty volume's root names %q, which is no HFS+ metadata", e.Name)
				}
			}
			if name == "journaled" && len(es) == 0 {
				t.Error("the journaled volume's root names no journal files: the walk missed them")
			}
		})
	}
}
