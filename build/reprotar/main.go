// Command reprotar packs a directory into a reproducible .tar.gz: the
// same tree gives the same bytes, whoever owns it, whatever its mtimes,
// its umask or the order its files were made in.
//
//	reprotar -root DIR -prefix opt/mavergreen/longboat -out FILE
//
// Every entry is named under the prefix, in bytewise order, as USTAR, owned
// by root:root (0:0), with SOURCE_DATE_EPOCH as its mtime, and mode 0755
// for a directory or a file with any execute bit and 0644 otherwise. The
// gzip header carries no name and no time. A symlink, any other special
// file and a name USTAR cannot hold are refused, and nothing is written.
//
// Exit codes: 0 ok, 1 refused or failed, 2 usage (SOURCE_DATE_EPOCH
// unset or malformed included).
package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxMtime is the largest time a USTAR header's 11 octal digits hold.
const maxMtime = 1<<33 - 1

const usage = "usage: SOURCE_DATE_EPOCH=<seconds> reprotar -root DIR -prefix PREFIX -out FILE"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr, os.Getenv))
}

// run is the command, given its arguments (without the program's name),
// where to say what went wrong, and its environment.
func run(args []string, stderr io.Writer, getenv func(string) string) int {
	fl := flag.NewFlagSet("reprotar", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	root := fl.String("root", "", "the directory to pack")
	prefix := fl.String("prefix", "", "the relative path every entry is named under")
	out := fl.String("out", "", "the .tar.gz to write")
	if err := fl.Parse(args); err != nil {
		fmt.Fprintf(stderr, "reprotar: %v\n%s\n", err, usage)
		return 2
	}
	if fl.NArg() != 0 || *root == "" || *prefix == "" || *out == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if !validPrefix(*prefix) {
		fmt.Fprintf(stderr, "reprotar: -prefix %q must be a clean relative path, such as opt/mavergreen/longboat\n", *prefix)
		return 2
	}
	mtime, err := sourceDateEpoch(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "reprotar: %v\n", err)
		return 2
	}
	if info, err := os.Stat(*root); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "reprotar: -root %s is not a directory\n", *root)
		return 1
	}
	if err := writeFile(*out, os.DirFS(*root), *prefix, mtime); err != nil {
		fmt.Fprintf(stderr, "reprotar: %v\n", err)
		return 1
	}
	return 0
}

// validPrefix says whether p is a clean relative path that stays below
// where the archive is extracted.
func validPrefix(p string) bool {
	return p != "." && p != ".." && !path.IsAbs(p) && path.Clean(p) == p && !strings.HasPrefix(p, "../")
}

// sourceDateEpoch reads SOURCE_DATE_EPOCH, which must be plain decimal
// seconds that a USTAR header can hold.
func sourceDateEpoch(getenv func(string) string) (time.Time, error) {
	v := getenv("SOURCE_DATE_EPOCH")
	if v == "" {
		return time.Time{}, errors.New("SOURCE_DATE_EPOCH is unset; set it to the release commit's time (git log -1 --format=%ct)")
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH %q is not a whole number of seconds", v)
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n > maxMtime {
		return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH %q is past what a USTAR header holds", v)
	}
	return time.Unix(n, 0), nil
}

// writeFile writes the archive beside out and renames it into place, so a
// refusal or a failure leaves nothing at out.
func writeFile(out string, fsys fs.FS, prefix string, mtime time.Time) (err error) {
	f, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if err = write(f, fsys, prefix, mtime); err != nil {
		return err
	}
	if err = f.Chmod(0o644); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), out)
}

// member is one entry of the archive. rel is its path in the tree, "."
// for the tree's root, which is the prefix's own entry.
type member struct {
	name, rel string
	dir, exec bool
}

// write packs fsys under prefix, gzipped, to w. Of a file's metadata it
// reads only its type and whether any execute bit is set.
func write(w io.Writer, fsys fs.FS, prefix string, mtime time.Time) error {
	var ms []member
	err := fs.WalkDir(fsys, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		m := member{rel: rel, name: prefix + "/"}
		if rel != "." {
			m.name += rel
		}
		switch t := d.Type(); {
		case t.IsDir():
			m.dir = true
			if rel != "." {
				m.name += "/"
			}
		case t&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink; the tarball carries only directories and regular files", rel)
		case !t.IsRegular():
			return fmt.Errorf("%s is a %v, not a regular file; the tarball carries only directories and regular files", rel, t.Type())
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			m.exec = info.Mode().Perm()&0o111 != 0
		}
		if !fitsUSTAR(m.name) {
			return fmt.Errorf("%s: a USTAR header cannot hold the name %s (ASCII only, at most 100 bytes after a slash and 155 before it)", rel, m.name)
		}
		ms = append(ms, m)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].name < ms[j].name })

	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	for _, m := range ms {
		h := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     m.name,
			Mode:     0o644,
			ModTime:  mtime,
			Uname:    "root",
			Gname:    "root",
			Format:   tar.FormatUSTAR,
		}
		var data []byte
		if m.dir {
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		} else {
			if m.exec {
				h.Mode = 0o755
			}
			if data, err = fs.ReadFile(fsys, m.rel); err != nil {
				return err
			}
			h.Size = int64(len(data))
		}
		if err := tw.WriteHeader(h); err != nil {
			return fmt.Errorf("%s: %w", m.rel, err)
		}
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("%s: %w", m.rel, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// fitsUSTAR says whether a USTAR header can hold name: ASCII with no NUL,
// and either at most 100 bytes or split at a slash into at most 155 bytes
// of prefix and 100 of name, as archive/tar splits it.
func fitsUSTAR(name string) bool {
	for i := 0; i < len(name); i++ {
		if name[i] == 0 || name[i] >= 0x80 {
			return false
		}
	}
	const nameSize, prefixSize = 100, 155
	if len(name) <= nameSize {
		return true
	}
	length := len(name)
	if length > prefixSize+1 {
		length = prefixSize + 1
	} else if name[length-1] == '/' {
		length--
	}
	i := strings.LastIndex(name[:length], "/")
	suffix := len(name) - i - 1
	return i > 0 && suffix > 0 && suffix <= nameSize && i <= prefixSize
}
