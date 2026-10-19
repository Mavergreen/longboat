package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Mavergreen/longboat/installmedia"
	"github.com/Mavergreen/longboat/installmedia/internal/fixture"
)

// cli runs the command with env as its whole environment.
func cli(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb, func(k string) string { return env[k] })
	return code, out.String(), errb.String()
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func discPkgs() map[string][]byte {
	return map[string][]byte{"A.pkg": []byte("package a"), "B.pkg": []byte("package b, longer")}
}

// fixtureRelease is a 10.6-like release pinned to pkgs on a disc of
// build 10ZFIX.
func fixtureRelease(pkgs map[string][]byte) installmedia.Release {
	d := installmedia.Disc{Build: "10ZFIX", Sums: map[string]string{}}
	for n, b := range pkgs {
		d.Sums[n] = sumOf(b)
	}
	return installmedia.Release{Name: "10.6", Codename: "snowleopard", FileName: "install-disc.img", Discs: []installmedia.Disc{d}}
}

func discBytes(pkgs map[string][]byte) []byte {
	return fixture.HFS(fixture.SnowLeopardDisc("10ZFIX", pkgs, nil), fixture.HFSOptions{})
}

// useRelease makes lookup answer rel for its name, and every other
// name as the library does.
func useRelease(t *testing.T, rel installmedia.Release) {
	t.Helper()
	old := lookup
	lookup = func(name string) (installmedia.Release, error) {
		if name == rel.Name {
			return rel, nil
		}
		return installmedia.Lookup(name)
	}
	t.Cleanup(func() { lookup = old })
}

func writeFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestUsageErrorsExitTwo(t *testing.T) {
	useRelease(t, fixtureRelease(discPkgs()))
	dir := t.TempDir()
	into := filepath.Join(dir, "into")
	disc := writeFile(t, dir, "disc.img", discBytes(discPkgs()))
	const url = "https://h.example/s3cr3t/disc.img?token=t0k"
	env := map[string]string{"H": "Authorization: Bearer h3ad3r"}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no arguments", nil, "usage:"},
		{"an unknown command", []string{"frobnicate"}, "usage:"},
		{"fetch alone", []string{"fetch"}, "usage:"},
		{"fetch alone", []string{"fetch"}, "usage:"},
		{"the old installer word", []string{"fetch", "installer", "10.9", "--into", into}, "usage:"},
		{"verify with the old installer word", []string{"verify", "installer", "10.6", disc}, "usage:"},
		{"an unknown release", []string{"fetch", "10.5", "--into", into, "--source-file", disc}, `no install media for version "10.5"`},
		{"no --into", []string{"fetch", "10.6", "--source-file", disc}, "--into"},
		{"--into without its DIR", []string{"fetch", "10.6", "--source-file", disc, "--into"}, "--into needs"},
		{"--into twice", []string{"fetch", "10.6", "--into", into, "--into", into, "--source-file", disc}, "--into"},
		{"--auth-header-env first", []string{"fetch", "10.6", "--into", into, "--auth-header-env", "H"}, "--source-url"},
		{"--auth-header-env after a file", []string{"fetch", "10.6", "--into", into, "--source-file", disc, "--auth-header-env", "H"}, "--source-url"},
		{"--auth-header-env twice", []string{"fetch", "10.6", "--into", into, "--source-url", url, "--auth-header-env", "H", "--auth-header-env", "H"}, "--source-url"},
		{"--auth-header-env naming an unset variable", []string{"fetch", "10.6", "--into", into, "--source-url", url, "--auth-header-env", "UNSET"}, "UNSET"},
		{"--auth-header-env given a header, not a name", []string{"fetch", "10.6", "--into", into, "--source-url", url, "--auth-header-env", "Authorization: Bearer h3ad3r"}, "environment variable"},
		{"an unknown flag", []string{"fetch", "10.6", "--into", into, "--source-disc", disc}, `"--source-disc"`},
		{"a stray argument that is a URL", []string{"fetch", "10.6", "--into", into, url}, "withheld"},
		{"a stray argument that is a header", []string{"fetch", "10.6", "--into", into, "Authorization: Bearer h3ad3r"}, "withheld"},
		{"a flag with a URL glued on", []string{"fetch", "10.6", "--into", into, "--source-url=" + url}, "withheld"},
		{"a file source given a URL", []string{"fetch", "10.6", "--into", into, "--source-file", url}, "looks like a URL"},
		{"a URL source that is not http", []string{"fetch", "10.6", "--into", into, "--source-url", "ftp://h.example/s3cr3t?token=t0k"}, "http"},
		{"a command given a URL", []string{"fetch", "10.6", "--into", into, "--source-command", url}, "withheld"},
		{"--into given a URL", []string{"fetch", "10.6", "--into", dir + "/" + url, "--source-file", disc}, "withheld"},
		{"a relative command", []string{"fetch", "10.6", "--into", into, "--source-command", "bin/make-disc"}, "absolute"},
		{"verify alone", []string{"verify"}, "usage:"},
		{"verify without an image", []string{"verify", "10.6"}, "usage:"},
		{"verify with two images", []string{"verify", "10.6", disc, disc}, "usage:"},
		{"verify an unknown release", []string{"verify", "10.5", disc}, `no install media for version "10.5"`},
		{"verify a URL", []string{"verify", "10.6", url}, "withheld"},
		{"a URL for a version", []string{"verify", url, disc}, "withheld"},
		{"--version with an argument", []string{"--version", "x"}, "usage:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := cli(t, env, tc.args...)
			if code != 2 {
				t.Errorf("exit %d, want 2 (stderr %q)", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout %q, want nothing", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr %q does not contain %q", stderr, tc.want)
			}
			for _, s := range []string{"s3cr3t", "t0k", "h3ad3r"} {
				if strings.Contains(stderr, s) {
					t.Errorf("stderr %q carries the secret %q", stderr, s)
				}
			}
			if left := entries(t, into); len(left) != 0 {
				t.Errorf("a usage error left %v in DIR", left)
			}
			if _, err := os.Stat(into); err == nil {
				t.Error("a usage error made DIR")
			}
		})
	}
}

func TestVersionAndHelp(t *testing.T) {
	old := version
	version = "20261014.1"
	t.Cleanup(func() { version = old })
	if code, stdout, stderr := cli(t, nil, "--version"); code != 0 || stdout != "installmedia 20261014.1\n" || stderr != "" {
		t.Errorf("--version: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr := cli(t, nil, "--help")
	if code != 0 || stderr != "" {
		t.Errorf("--help: exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"longboat media list", "longboat media fetch <version> --into DIR", "--source-recipe", "--source-file PATH",
		"--source-url URL [--auth-header-env NAME]", "--source-command PATH", "longboat media verify <version> <image>"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--help's %q does not mention %q", stdout, want)
		}
	}
}

func TestVerifyPrintsTheDelivery(t *testing.T) {
	pkgs := discPkgs()
	rel := fixtureRelease(pkgs)
	useRelease(t, rel)
	disc := writeFile(t, t.TempDir(), "disc.img", discBytes(pkgs))
	code, stdout, stderr := cli(t, nil, "verify", "10.6", disc)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if want := "10.6 10ZFIX " + installmedia.DiscDigest(rel.Discs[0]) + "\n"; stdout != want {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr %q, want nothing", stderr)
	}
}

func TestVerifyRefusalExitsOne(t *testing.T) {
	pkgs := discPkgs()
	useRelease(t, fixtureRelease(pkgs))
	other := discPkgs()
	other["B.pkg"] = []byte("package b, altered")
	mismatched := writeFile(t, t.TempDir(), "disc.img", discBytes(other))
	for _, tc := range []struct{ name, image, want string }{
		{"/dev/null", os.DevNull, "not a disc image"},
		{"a disc that differs", mismatched, "B.pkg"},
		{"a disc that is not there", filepath.Join(t.TempDir(), "absent.img"), "absent.img"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := cli(t, nil, "verify", "10.6", tc.image)
			if code != 1 {
				t.Errorf("exit %d, want 1 (stderr %q)", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout %q, want nothing", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr %q does not name the problem (%q)", stderr, tc.want)
			}
		})
	}
}

func TestFetchWithoutASourceForTenSix(t *testing.T) {
	into := filepath.Join(t.TempDir(), "into")
	code, stdout, stderr := cli(t, nil, "fetch", "10.6", "--into", into)
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if stdout != "" || !strings.Contains(stderr, "version 10.6 has no built-in recipe") {
		t.Errorf("stdout %q, stderr %q, want a refusal naming no built-in recipe", stdout, stderr)
	}
	if _, err := os.Stat(into); err == nil {
		t.Error("a refusal made DIR")
	}
}

// server serves body, keeping every Authorization header it saw.
type server struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func serve(t *testing.T, status int, body []byte) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths...)
}

func TestTheAuthHeaderComesOnlyFromTheEnvironment(t *testing.T) {
	pkgs := discPkgs()
	useRelease(t, fixtureRelease(pkgs))
	env := map[string]string{"H": "Authorization: Bearer h3ad3r"}
	for _, tc := range []struct {
		name   string
		status int
		code   int
	}{
		{"served", http.StatusOK, 0},
		{"forbidden", http.StatusForbidden, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, tc.status, discBytes(pkgs))
			url := srv.URL + "/s3cr3t/disc.img?token=t0k"
			into := filepath.Join(t.TempDir(), "into")
			code, stdout, stderr := cli(t, env, "fetch", "10.6", "--into", into, "--source-url", url, "--auth-header-env", "H")
			if code != tc.code {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.code, stderr)
			}
			if got := srv.seen(); len(got) == 0 || got[0] != "Bearer h3ad3r" {
				t.Errorf("the server saw Authorization %q, want the environment's header", got)
			}
			if tc.code == 0 {
				if want := filepath.Join(into, "install-disc.img") + "\n"; stdout != want {
					t.Errorf("stdout %q, want %q", stdout, want)
				}
			} else if !strings.Contains(stderr, "file at a URL (withheld)") {
				t.Errorf("stderr %q does not name the source as withheld", stderr)
			}
			for _, s := range []string{"s3cr3t", "t0k", "h3ad3r", "Bearer", srv.Listener.Addr().String()} {
				if strings.Contains(stdout, s) || strings.Contains(stderr, s) {
					t.Errorf("stdout %q or stderr %q carries the secret %q", stdout, stderr, s)
				}
			}
		})
	}
}

func TestAnUnpinnedFileIsPlacedIntoDir(t *testing.T) {
	pkgs := discPkgs()
	useRelease(t, fixtureRelease(pkgs))
	good := discBytes(pkgs)
	other := discPkgs()
	other["A.pkg"] = []byte("package a, altered")

	t.Run("passes", func(t *testing.T) {
		src := writeFile(t, t.TempDir(), "my disc.img", good)
		into := filepath.Join(t.TempDir(), "into")
		code, stdout, stderr := cli(t, nil, "fetch", "10.6", "--into", into, "--source-file", src)
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		dest := filepath.Join(into, "install-disc.img")
		if stdout != dest+"\n" {
			t.Errorf("stdout %q, want %q", stdout, dest+"\n")
		}
		if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, good) {
			t.Errorf("DIR/install-disc.img is not the source's bytes (%v)", err)
		}
		if fi, err := os.Stat(dest); err != nil || fi.Mode() != 0o644 {
			t.Errorf("DIR/install-disc.img: %v, %v, want mode 0644", fi, err)
		}
		if left := entries(t, into); strings.Join(left, " ") != "install-disc.img" {
			t.Errorf("DIR holds %v, want only install-disc.img", left)
		}
		if got, err := os.ReadFile(src); err != nil || !bytes.Equal(got, good) {
			t.Errorf("the source file changed (%v)", err)
		}
	})
	t.Run("fails", func(t *testing.T) {
		src := writeFile(t, t.TempDir(), "my disc.img", discBytes(other))
		into := t.TempDir()
		code, stdout, stderr := cli(t, nil, "fetch", "10.6", "--into", into, "--source-file", src)
		if code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
		if stdout != "" || !strings.Contains(stderr, "A.pkg") {
			t.Errorf("stdout %q, stderr %q, want a refusal naming A.pkg", stdout, stderr)
		}
		if left := entries(t, into); len(left) != 0 {
			t.Errorf("a refused disc left %v in DIR", left)
		}
		if _, err := os.Stat(into); err != nil {
			t.Errorf("a refused disc took away a DIR that was already there: %v", err)
		}
	})
	t.Run("fails into a DIR it made", func(t *testing.T) {
		src := writeFile(t, t.TempDir(), "my disc.img", discBytes(other))
		parent := t.TempDir()
		into := filepath.Join(parent, "a", "b")
		code, _, _ := cli(t, nil, "fetch", "10.6", "--into", into, "--source-file", src)
		if code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
		if left := entries(t, parent); len(left) != 0 {
			t.Errorf("a refused disc left %v where DIR would have been: the directories it made go too", left)
		}
	})
	t.Run("already in DIR", func(t *testing.T) {
		into := t.TempDir()
		dest := writeFile(t, into, "install-disc.img", good)
		code, stdout, stderr := cli(t, nil, "fetch", "10.6", "--into", into, "--source-file", dest)
		if code != 0 || stdout != dest+"\n" {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if left := entries(t, into); strings.Join(left, " ") != "install-disc.img" {
			t.Errorf("DIR holds %v, want only install-disc.img", left)
		}
	})
}

func TestSourcesAreTriedInTheOrderGiven(t *testing.T) {
	pkgs := discPkgs()
	useRelease(t, fixtureRelease(pkgs))
	other := discPkgs()
	other["A.pkg"] = []byte("package a, altered")
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.img", discBytes(other))
	good := writeFile(t, dir, "good.img", discBytes(pkgs))
	into := filepath.Join(dir, "into")
	code, stdout, stderr := cli(t, nil, "fetch", "10.6", "--into", into, "--source-file", bad, "--source-file", good)
	if code != 0 || stdout != filepath.Join(into, "install-disc.img")+"\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	ib, ig := strings.Index(stderr, "bad.img"), strings.Index(stderr, "good.img")
	if ib < 0 || ig < 0 || ib > ig {
		t.Errorf("stderr %q does not show bad.img tried, and failing, before good.img", stderr)
	}
	if !strings.Contains(stderr, "longboat media: version 10.6: ") || strings.Contains(stderr, "release") {
		t.Errorf("stderr %q does not speak as longboat media of version 10.6", stderr)
	}
}

func TestACommandsDiscIsMovedIntoDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command sources need a unix host")
	}
	pkgs := discPkgs()
	useRelease(t, fixtureRelease(pkgs))
	dir := t.TempDir()
	good := writeFile(t, dir, "good.img", discBytes(pkgs))
	script := filepath.Join(dir, "make-disc")
	body := "#!/bin/sh\nfor a; do out=$a; done\necho making a disc\ncp '" + good + "' \"$out/disc.img\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(dir, "into")
	code, stdout, stderr := cli(t, nil, "fetch", "10.6", "--into", into, "--source-command", script)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	dest := filepath.Join(into, "install-disc.img")
	if stdout != dest+"\n" {
		t.Errorf("stdout %q, want %q", stdout, dest+"\n")
	}
	if !strings.Contains(stderr, "making a disc") {
		t.Errorf("the command's output %q did not reach stderr", stderr)
	}
	if left := entries(t, into); strings.Join(left, " ") != "install-disc.img" {
		t.Errorf("DIR holds %v, want only install-disc.img: the command's scratch goes", left)
	}
}

func TestAnUncleanIntoStillWorks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command sources need a unix host")
	}
	pkgs := discPkgs()
	useRelease(t, fixtureRelease(pkgs))
	dir := t.TempDir()
	good := writeFile(t, dir, "good.img", discBytes(pkgs))
	script := filepath.Join(dir, "make-disc")
	body := "#!/bin/sh\nfor a; do out=$a; done\ncp '" + good + "' \"$out/disc.img\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	into := root + "/x/../y"
	code, stdout, stderr := cli(t, nil, "fetch", "10.6", "--into", into, "--source-command", script)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	dest := filepath.Join(root, "y", "install-disc.img")
	if stdout != dest+"\n" {
		t.Errorf("stdout %q, want %q", stdout, dest+"\n")
	}
	if left := entries(t, filepath.Join(root, "y")); strings.Join(left, " ") != "install-disc.img" {
		t.Errorf("R/y holds %v, want only install-disc.img", left)
	}
}

// spec: .superpowers/specs/2026-10-19-longboat-two-jobs-design.md "longboat list" -- media list is one line
// per release, oldest first, saying whether it has a built-in recipe
func TestListOldestFirstWithRecipe(t *testing.T) {
	code, stdout, stderr := cli(t, nil, "list")
	if code != 0 || stderr != "" {
		t.Fatalf("list: exit %d, stderr %q", code, stderr)
	}
	if want := "10.6 bring-your-own\n10.9 recipe\n"; stdout != want {
		t.Errorf("list printed %q, want %q", stdout, want)
	}
	if code, _, _ := cli(t, nil, "list", "10.9"); code != 2 {
		t.Errorf("list with an argument exited %d, want 2", code)
	}
}

// spec: the design's "The command line" -- usage and errors name the longboat media forms and say
// <version>, and installmedia is never what the user is told they ran
func TestUsageNamesLongboatMedia(t *testing.T) {
	into := filepath.Join(t.TempDir(), "into")
	_, _, stderr := cli(t, nil, "fetch", "10.5", "--into", into)
	if !strings.HasPrefix(stderr, "longboat media: ") {
		t.Errorf("stderr %q does not start with %q", stderr, "longboat media: ")
	}
	for _, want := range []string{"longboat media fetch <version>", "longboat media verify <version> <image>", "offered: 10.6 10.9"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q does not contain %q", stderr, want)
		}
	}
	for _, not := range []string{"installmedia fetch", "installer <release>", "release"} {
		if strings.Contains(stderr, not) {
			t.Errorf("stderr %q still says %q", stderr, not)
		}
	}
}
