package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const (
	epoch  = "1700000000"
	prefix = "opt/mavergreen/longboat"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func withEpoch() func(string) string { return env(map[string]string{"SOURCE_DATE_EPOCH": epoch}) }

// entry is one thing in a test tree: a directory when path ends in "/".
type entry struct {
	path, content string
	exec          bool
}

var sample = []entry{
	{"bin/longboat", "#!/bin/sh\necho longboat\n", true},
	{"libexec/longboat/fetch.sh", "fetch\n", false},
	{"libexec/longboat/installmedia", "\x7fELF not really\n", true},
	{"libexec/longboat/VERSION", "20261014.1\n", false},
	// A walk visits libexec/longboat/ and all it holds first; bytewise,
	// "." sorts before "/", so this comes first.
	{"libexec/longboat.txt", "notes\n", false},
	{"libexec/longboat/shipyard/templates/base-postinstall.sh", "post\n", false},
	{"share/empty/", "", false},
}

// layout says how lay makes a tree: the permission bits a umask would
// leave, the mtime every file gets, and the order it creates them in.
type layout struct {
	dirPerm, filePerm, execPerm os.FileMode
	mtime                       time.Time
	reverse                     bool
}

// lay makes sample under a fresh directory, as l says, and returns it.
func lay(t *testing.T, l layout) string {
	t.Helper()
	root := t.TempDir()
	es := slices.Clone(sample)
	if l.reverse {
		slices.Reverse(es)
	}
	for _, e := range es {
		p := filepath.Join(root, filepath.FromSlash(e.path))
		if strings.HasSuffix(e.path, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(e.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Permissions and times go on last, deepest first, so that making a
	// child never changes its directory's afterwards.
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		paths = append(paths, p)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(paths)
	execs := map[string]bool{}
	for _, e := range sample {
		execs[filepath.Join(root, filepath.FromSlash(e.path))] = e.exec
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		perm := l.filePerm
		switch {
		case info.IsDir():
			perm = l.dirPerm
		case execs[p]:
			perm = l.execPerm
		}
		if err := os.Chmod(p, perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, l.mtime, l.mtime); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// pack runs reprotar on root and returns the archive's bytes.
func pack(t *testing.T, root string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "x.tar.gz")
	var stderr bytes.Buffer
	if code := run([]string{"-root", root, "-prefix", prefix, "-out", out}, &stderr, withEpoch()); code != 0 {
		t.Fatalf("reprotar exited %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// mapTree is sample as an fs.FS whose every file names owner as its
// owner, the way archive/tar.FileInfoHeader would read one.
func mapTree(owner *tar.Header) fstest.MapFS {
	m := fstest.MapFS{}
	for _, e := range sample {
		if strings.HasSuffix(e.path, "/") {
			m[strings.TrimSuffix(e.path, "/")] = &fstest.MapFile{Mode: fs.ModeDir | 0o700, Sys: owner}
			continue
		}
		mode := fs.FileMode(0o600)
		if e.exec {
			mode = 0o700
		}
		m[e.path] = &fstest.MapFile{Data: []byte(e.content), Mode: mode, ModTime: time.Unix(1234, 0), Sys: owner}
	}
	return m
}

func TestSameTreeSameBytes(t *testing.T) {
	a := pack(t, lay(t, layout{dirPerm: 0o775, filePerm: 0o664, execPerm: 0o775, mtime: time.Now()}))
	b := pack(t, lay(t, layout{dirPerm: 0o700, filePerm: 0o600, execPerm: 0o700, mtime: time.Unix(1e9, 0), reverse: true}))
	if len(a) == 0 {
		t.Fatal("reprotar wrote an empty archive")
	}
	if digest(a) != digest(b) {
		t.Errorf("two trees with the same content packed differently: %s and %s", digest(a), digest(b))
	}

	for _, owner := range []*tar.Header{
		{Uid: 501, Gid: 20, Uname: "schmonz", Gname: "staff"},
		{Uid: 1001, Gid: 118, Uname: "runner", Gname: "docker"},
	} {
		var buf bytes.Buffer
		if err := write(&buf, mapTree(owner), prefix, time.Unix(1700000000, 0)); err != nil {
			t.Fatal(err)
		}
		if digest(buf.Bytes()) != digest(a) {
			t.Errorf("a tree owned by %s:%s packed differently from one on disk", owner.Uname, owner.Gname)
		}
	}
}

type packed struct {
	hdr     *tar.Header
	content string
}

func unpack(t *testing.T, b []byte) (*gzip.Reader, []packed) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var got []packed
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		c, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, packed{h, string(c)})
	}
	return zr, got
}

func TestHeadersAreNormalized(t *testing.T) {
	b := pack(t, lay(t, layout{dirPerm: 0o700, filePerm: 0o600, execPerm: 0o700, mtime: time.Unix(1e9, 0), reverse: true}))

	if len(b) < 10 || !bytes.Equal(b[4:8], []byte{0, 0, 0, 0}) || b[8] != 2 {
		t.Errorf("gzip header carries a time or is not at best compression: % x", b[:10])
	}
	zr, got := unpack(t, b)
	if zr.Name != "" || zr.Comment != "" || !zr.ModTime.IsZero() || zr.Extra != nil {
		t.Errorf("gzip header carries a name, comment, time or extra field: %+v", zr.Header)
	}

	want := map[string]entry{prefix + "/": {path: "/"}}
	for _, e := range sample {
		want[prefix+"/"+e.path] = e
		for d := filepath.ToSlash(filepath.Dir(strings.TrimSuffix(e.path, "/"))); d != "."; d = filepath.ToSlash(filepath.Dir(d)) {
			want[prefix+"/"+d+"/"] = entry{path: d + "/"}
		}
	}
	var names []string
	for _, p := range got {
		h := p.hdr
		names = append(names, h.Name)
		e, ok := want[h.Name]
		if !ok {
			t.Errorf("unexpected entry %q", h.Name)
			continue
		}
		delete(want, h.Name)
		if !strings.HasPrefix(h.Name, prefix+"/") {
			t.Errorf("%s is not under %s/", h.Name, prefix)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "root" || h.Gname != "root" {
			t.Errorf("%s: owner %d:%d %s:%s, want 0:0 root:root", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if h.ModTime.Unix() != 1700000000 || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
			t.Errorf("%s: times %v %v %v, want only an mtime of SOURCE_DATE_EPOCH", h.Name, h.ModTime, h.AccessTime, h.ChangeTime)
		}
		if h.Format != tar.FormatUSTAR || h.PAXRecords != nil {
			t.Errorf("%s: format %v with PAX records %v, want plain USTAR", h.Name, h.Format, h.PAXRecords)
		}
		wantType, wantMode := byte(tar.TypeReg), int64(0o644)
		switch {
		case strings.HasSuffix(e.path, "/"):
			wantType, wantMode = tar.TypeDir, 0o755
		case e.exec:
			wantMode = 0o755
		}
		if h.Typeflag != wantType || h.Mode != wantMode {
			t.Errorf("%s: type %q mode %#o, want %q %#o", h.Name, h.Typeflag, h.Mode, wantType, wantMode)
		}
		if p.content != e.content {
			t.Errorf("%s: content %q, want %q", h.Name, p.content, e.content)
		}
	}
	for n := range want {
		t.Errorf("missing entry %q", n)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("entries are not sorted bytewise: %q", names)
	}
}

// refused runs reprotar on root and wants it to fail naming why, and to
// leave nothing where its archive would have gone.
func refused(t *testing.T, root string, why ...string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "x.tar.gz")
	var stderr bytes.Buffer
	if code := run([]string{"-root", root, "-prefix", prefix, "-out", out}, &stderr, withEpoch()); code != 1 {
		t.Errorf("reprotar exited %d, want 1: %s", code, stderr.String())
	}
	for _, w := range why {
		if !strings.Contains(stderr.String(), w) {
			t.Errorf("the refusal does not say %q: %s", w, stderr.String())
		}
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("a refused archive left %v behind", left)
	}
}

func TestRefusesSymlinksAndLongNames(t *testing.T) {
	file := func(t *testing.T, root, rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		file(t, root, "bin/longboat")
		if err := os.Symlink("../bin/longboat", filepath.Join(root, "bin", "lb")); err != nil {
			t.Fatal(err)
		}
		refused(t, root, "bin/lb", "symlink")
	})

	t.Run("a name over 100 bytes after its last slash", func(t *testing.T) {
		root := t.TempDir()
		long := strings.Repeat("n", 101)
		file(t, root, "libexec/"+long)
		refused(t, root, long, "USTAR")
	})

	t.Run("a directory name over 100 bytes", func(t *testing.T) {
		root := t.TempDir()
		long := strings.Repeat("d", 101)
		if err := os.MkdirAll(filepath.Join(root, long), 0o755); err != nil {
			t.Fatal(err)
		}
		refused(t, root, long, "USTAR")
	})

	t.Run("a path over 256 bytes", func(t *testing.T) {
		root := t.TempDir()
		rel := strings.Repeat(strings.Repeat("p", 60)+"/", 4) + "f"
		file(t, root, rel)
		refused(t, root, "USTAR")
	})

	t.Run("a name that is not ASCII", func(t *testing.T) {
		root := t.TempDir()
		file(t, root, "café")
		refused(t, root, "café", "USTAR")
	})

	t.Run("a named pipe", func(t *testing.T) {
		m := fstest.MapFS{"bin/fifo": &fstest.MapFile{Mode: fs.ModeNamedPipe | 0o644}}
		err := write(io.Discard, m, prefix, time.Unix(1700000000, 0))
		if err == nil || !strings.Contains(err.Error(), "bin/fifo") {
			t.Errorf("a named pipe gave %v, want a refusal naming it", err)
		}
	})

	t.Run("a long path that USTAR's prefix field holds", func(t *testing.T) {
		root := t.TempDir()
		rel := strings.Repeat(strings.Repeat("q", 60)+"/", 2) + strings.Repeat("f", 90)
		file(t, root, rel)
		_, got := unpack(t, pack(t, root))
		if n := got[len(got)-1].hdr.Name; n != prefix+"/"+rel {
			t.Errorf("the last entry is %q, want %q", n, prefix+"/"+rel)
		}
	})
}

func TestRequiresSourceDateEpoch(t *testing.T) {
	root := lay(t, layout{dirPerm: 0o755, filePerm: 0o644, execPerm: 0o755, mtime: time.Now()})
	for name, getenv := range map[string]func(string) string{
		"unset":               env(nil),
		"empty":               env(map[string]string{"SOURCE_DATE_EPOCH": ""}),
		"not a number":        env(map[string]string{"SOURCE_DATE_EPOCH": "yesterday"}),
		"negative":            env(map[string]string{"SOURCE_DATE_EPOCH": "-1"}),
		"fractional":          env(map[string]string{"SOURCE_DATE_EPOCH": "1.5"}),
		"hex":                 env(map[string]string{"SOURCE_DATE_EPOCH": "0x10"}),
		"padded":              env(map[string]string{"SOURCE_DATE_EPOCH": " 1700000000"}),
		"beyond USTAR's 8^11": env(map[string]string{"SOURCE_DATE_EPOCH": "8589934592"}),
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "x.tar.gz")
			var stderr bytes.Buffer
			if code := run([]string{"-root", root, "-prefix", prefix, "-out", out}, &stderr, getenv); code != 2 {
				t.Errorf("reprotar exited %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "SOURCE_DATE_EPOCH") {
				t.Errorf("the refusal does not name SOURCE_DATE_EPOCH: %s", stderr.String())
			}
			if _, err := os.Stat(out); err == nil {
				t.Error("reprotar wrote an archive without a SOURCE_DATE_EPOCH")
			}
		})
	}
}

func TestUsage(t *testing.T) {
	root := lay(t, layout{dirPerm: 0o755, filePerm: 0o644, execPerm: 0o755, mtime: time.Now()})
	out := filepath.Join(t.TempDir(), "x.tar.gz")
	for name, args := range map[string][]string{
		"no root":            {"-prefix", prefix, "-out", out},
		"no prefix":          {"-root", root, "-out", out},
		"no out":             {"-root", root, "-prefix", prefix},
		"an extra argument":  {"-root", root, "-prefix", prefix, "-out", out, "more"},
		"an absolute prefix": {"-root", root, "-prefix", "/" + prefix, "-out", out},
		"a prefix with ..":   {"-root", root, "-prefix", "opt/../etc", "-out", out},
		"a prefix ending /":  {"-root", root, "-prefix", prefix + "/", "-out", out},
		"a prefix of .":      {"-root", root, "-prefix", ".", "-out", out},
		"an unknown flag":    {"-root", root, "-prefix", prefix, "-out", out, "-v"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run(args, &stderr, withEpoch()); code != 2 {
				t.Errorf("reprotar exited %d, want 2: %s", code, stderr.String())
			}
			if _, err := os.Stat(out); err == nil {
				t.Error("reprotar wrote an archive on a usage error")
			}
		})
	}

	t.Run("a root that is not a directory", func(t *testing.T) {
		var stderr bytes.Buffer
		if code := run([]string{"-root", filepath.Join(root, "bin", "longboat"), "-prefix", prefix, "-out", out}, &stderr, withEpoch()); code != 1 {
			t.Errorf("reprotar exited %d, want 1: %s", code, stderr.String())
		}
	})

	t.Run("an existing archive is replaced", func(t *testing.T) {
		if err := os.WriteFile(out, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		if code := run([]string{"-root", root, "-prefix", prefix, "-out", out}, &stderr, withEpoch()); code != 0 {
			t.Fatalf("reprotar exited %d: %s", code, stderr.String())
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if _, got := unpack(t, b); len(got) == 0 {
			t.Error("the replaced archive is empty")
		}
	})
}
