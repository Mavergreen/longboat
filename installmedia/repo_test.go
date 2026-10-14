package installmedia

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maxFile is the largest file the module may carry. Its biggest real
// file, the Snow Leopard package pins, is under 30 KiB.
const maxFile = 2 << 20

var imageExts = []string{".dmg", ".iso", ".img", ".pkg", ".sparseimage"}

// imageMagics are the signatures of HFS+ and HFSX volume headers, UDIF
// trailers and xar archives (a flat .pkg).
var imageMagics = []struct{ magic, what string }{
	{"H+\x00\x04", "an HFS+ volume header"},
	{"HX\x00\x05", "an HFSX volume header"},
	{"koly", "a UDIF trailer"},
	{"xar!", "a xar archive (a flat package)"},
}

// diskImageReasons says why the files under root look like disc images,
// packages or anything else too big to be source, one line per reason.
func diskImageReasons(root string) ([]string, error) {
	var reasons []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		lower := strings.ToLower(d.Name())
		for _, ext := range imageExts {
			if strings.HasSuffix(lower, ext) {
				reasons = append(reasons, path+": named like a disc image or package ("+ext+")")
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFile {
			reasons = append(reasons, path+": larger than 2 MiB")
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		// A volume's header is at byte 1024 and a UDIF image's koly
		// trailer is its last 512 bytes, so look there as well as at
		// the start.
		offsets := []int64{0, 1024}
		if info.Size() >= 512 {
			offsets = append(offsets, info.Size()-512)
		}
		for _, off := range offsets {
			head := make([]byte, 4)
			if _, err := f.ReadAt(head, off); err != nil && err != io.EOF {
				return err
			}
			for _, m := range imageMagics {
				if bytes.Equal(head, []byte(m.magic)) {
					reasons = append(reasons, fmt.Sprintf("%s: holds %s at byte %d", path, m.what, off))
				}
			}
		}
		return nil
	})
	return reasons, err
}

func TestTheModuleCarriesNoDiskImages(t *testing.T) {
	reasons, err := diskImageReasons(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reasons {
		t.Errorf("%s: no Apple bytes, anywhere -- build fixtures at test time", r)
	}
}

func TestTheDiskImageGuardCatchesEachShape(t *testing.T) {
	at := func(off int, magic string) []byte {
		b := make([]byte, off+len(magic)+100)
		copy(b[off:], magic)
		return b
	}
	trailer := make([]byte, 4096)
	copy(trailer[len(trailer)-512:], "koly")
	for name, content := range map[string][]byte{
		"installer.DMG":       []byte("x"),
		"disc.iso":            []byte("x"),
		"install-disc.img":    []byte("x"),
		"BSD.pkg":             []byte("x"),
		"scratch.sparseimage": []byte("x"),
		"hfsplus":             at(0, "H+\x00\x04"),
		"hfsplus-volume":      at(1024, "H+\x00\x04"),
		"hfsx":                at(0, "HX\x00\x05"),
		"udif-start":          at(0, "koly"),
		"udif-trailer":        trailer,
		"flat-package":        at(0, "xar!"),
		"big":                 make([]byte, maxFile+1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
				t.Fatal(err)
			}
			reasons, err := diskImageReasons(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(reasons) == 0 {
				t.Errorf("the guard passed %s", name)
			}
		})
	}
	t.Run("ordinary files pass", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("H+ is HFS+'s signature\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git", "pack.img"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		reasons, err := diskImageReasons(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(reasons) != 0 {
			t.Errorf("the guard refused ordinary files: %v", reasons)
		}
	})
}
