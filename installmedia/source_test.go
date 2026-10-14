package installmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Mavergreen/longboat/installmedia/download"
	"github.com/Mavergreen/longboat/installmedia/internal/fixture"
)

// server serves body to every request, counting them and keeping the
// last Authorization header it saw.
type server struct {
	*httptest.Server
	requests atomic.Int32
	mu       sync.Mutex
	auth     string
}

func serve(t *testing.T, status int, body []byte) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		s.mu.Lock()
		s.auth = r.Header.Get("Authorization")
		s.mu.Unlock()
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) lastAuth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth
}

// logs collects every line logged through it, from any goroutine.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) log(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *logs) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// sourceOptions is Options with a Getter whose cache is a temp directory,
// laid out cache/<key>/<name>, and a WorkDir beside it.
func sourceOptions(t *testing.T) (Options, *logs) {
	t.Helper()
	root := t.TempDir()
	l := &logs{}
	g := &download.Getter{
		Path:    func(key, name string) string { return filepath.Join(root, "cache", key, name) },
		Log:     l.log,
		Retries: -1,
		Backoff: time.Millisecond,
	}
	return Options{Getter: g, WorkDir: filepath.Join(root, "work"), Log: l.log}, l
}

// pinnedRelease is a 10.9-like release pinned to content, whose recipe
// fetches its Whole.URL from recipe and counts its calls.
func pinnedRelease(content []byte, recipe *server) (Release, *atomic.Int32) {
	calls := &atomic.Int32{}
	rel := Release{Name: "10.9", Codename: "mavericks", FileName: "InstallESD.dmg",
		Whole: &WholeFile{Name: "fixture-esd", URL: recipe.URL + "/recipe/InstallESD.dmg", SHA256: sumOf(content), Build: "13ZFIX"},
		Recipe: func(ctx context.Context, g *download.Getter, want download.Item) (string, error) {
			calls.Add(1)
			return g.Get(ctx, want)
		},
	}
	return rel, calls
}

// smallPkgs keep the fixture disc small, so building it is quick.
func smallPkgs() map[string][]byte {
	return map[string][]byte{"A.pkg": []byte("package a"), "B.pkg": []byte("package b, longer")}
}

// unpinnedDisc is a 10.6-like release with no whole-file pin, and the
// bytes of a disc that passes its check.
func unpinnedDisc() (Release, []byte) {
	pkgs := smallPkgs()
	return fixtureRelease(pkgs), fixture.HFS(fixture.SnowLeopardDisc("10ZFIX", pkgs, nil), fixture.HFSOptions{})
}

// writeScript writes an executable /bin/sh script into a temp directory
// of its own and returns its absolute path. $out in body is the output
// directory: the last argument.
func writeScript(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("command sources need a unix host")
	}
	p := filepath.Join(t.TempDir(), name)
	script := "#!/bin/sh\nfor a; do out=$a; done\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
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
	return names
}

func modeOf(t *testing.T, p string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

func TestValidate(t *testing.T) {
	const secretURL = "https://h.example/s3cr3t/disc.img?token=t0k"
	const header = "Authorization: Bearer h3ad3r"
	for _, tc := range []struct {
		name string
		s    Source
		want string
	}{
		{"no kind", Source{URL: secretURL, AuthHeader: header}, "kind is required: recipe, file or command"},
		{"recipe with a path", Source{Kind: KindRecipe, Path: "/x.img"}, "recipe"},
		{"recipe with a URL", Source{Kind: KindRecipe, URL: secretURL, AuthHeader: header}, "recipe"},
		{"file with a path and a URL", Source{Kind: KindFile, Path: "/x.img", URL: secretURL}, "a path or a URL, not both"},
		{"file with neither", Source{Kind: KindFile}, "a path or a URL"},
		{"a file:// URL", Source{Kind: KindFile, URL: "file:///s3cr3t/disc.img?token=t0k", AuthHeader: header}, "http"},
		{"an ftp:// URL", Source{Kind: KindFile, URL: "ftp://h.example/s3cr3t?token=t0k"}, "http"},
		{"a URL that does not parse", Source{Kind: KindFile, URL: "https://h.example/s3cr3t?token=t0k%zz\x7f"}, "URL"},
		{"an auth header without a URL", Source{Kind: KindFile, Path: "/x.img", AuthHeader: header}, "auth header"},
		{"an auth header with no colon", Source{Kind: KindFile, URL: secretURL, AuthHeader: "Bearer h3ad3r"}, `"Name: value"`},
		{"an auth header with no value", Source{Kind: KindFile, URL: secretURL, AuthHeader: "Authorization:   "}, `"Name: value"`},
		{"an auth header with a bad name", Source{Kind: KindFile, URL: secretURL, AuthHeader: "Auth orization: Bearer h3ad3r"}, `"Name: value"`},
		{"an auth header carrying a newline", Source{Kind: KindFile, URL: secretURL, AuthHeader: "Authorization: Bearer h3ad3r\r\nX-Other: 1"}, `"Name: value"`},
		{"file with a command", Source{Kind: KindFile, Path: "/x.img", Command: []string{"/bin/true"}}, "command"},
		{"command with nothing to run", Source{Kind: KindCommand}, "command"},
		{"command with a relative program", Source{Kind: KindCommand, Command: []string{"bin/make-disc"}}, "absolute"},
		{"command with a URL for a program", Source{Kind: KindCommand, Command: []string{"https://h.example/s3cr3t/x.sh?token=t0k"}}, "URL"},
		{"command with a URL", Source{Kind: KindCommand, Command: []string{"/bin/true"}, URL: secretURL, AuthHeader: header}, "command"},
		{"command with a path", Source{Kind: KindCommand, Command: []string{"/bin/true"}, Path: "/x.img"}, "command"},
		{"a URL given as a file's path", Source{Kind: KindFile, Path: "https://h.example/s3cr3t/disc.img?token=t0k"}, "looks like a URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.s.Validate()
			if err == nil {
				t.Fatal("Validate accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate = %q, want it to contain %q", err, tc.want)
			}
			for _, secret := range []string{"s3cr3t", "t0k", "h3ad3r"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("Validate's error %q carries %q", err, secret)
				}
			}
		})
	}
	for _, s := range []Source{
		{Kind: KindRecipe},
		{Kind: KindFile, Path: "/x.img"},
		{Kind: KindFile, Path: "relative/x.img"},
		{Kind: KindFile, URL: secretURL},
		{Kind: KindFile, URL: "HTTP://h.example/x.img", AuthHeader: header},
		{Kind: KindFile, URL: secretURL, AuthHeader: "X-Token:h3ad3r"},
		{Kind: KindCommand, Command: []string{"/usr/local/bin/make-disc", "--fast"}},
	} {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%s) = %v, want it accepted", s, err)
		}
	}
}

func TestSourceString(t *testing.T) {
	for s, want := range map[*Source]string{
		{Kind: KindRecipe}:                                         "recipe",
		{Kind: KindFile, Path: "/d/x.img"}:                         "file /d/x.img",
		{Kind: KindFile, URL: "https://h/s3cr3t?token=t0k"}:        "file at a URL (withheld)",
		{Kind: KindCommand, Command: []string{"/opt/bin/mk", "a"}}: "command mk",
		{Kind: KindFile, Path: "https://h/s3cr3t?token=t0k"}:       "file at a URL-like path (withheld)",
		{Kind: "script", URL: "https://h/s3cr3t?token=t0k"}:        `kind "script"`,
	} {
		if got := s.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

func TestDefaultSources(t *testing.T) {
	mav, err := Lookup("10.9")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DefaultSources(mav)
	if err != nil || len(got) != 1 || got[0].Kind != KindRecipe {
		t.Errorf("DefaultSources(10.9) = %+v, %v, want [recipe]", got, err)
	}
	sl, err := Lookup("10.6")
	if err != nil {
		t.Fatal(err)
	}
	got, err = DefaultSources(sl)
	if err == nil || err.Error() != "release 10.6 has no built-in recipe: name a file or command source" {
		t.Errorf("DefaultSources(10.6) = %+v, %v", got, err)
	}
}

func TestObtainByKind(t *testing.T) {
	ctx := context.Background()
	content := []byte("the pinned InstallESD.dmg's bytes")

	t.Run("pinned recipe", func(t *testing.T) {
		o, _ := sourceOptions(t)
		rel, calls := pinnedRelease(content, serve(t, 200, content))
		c, err := Obtain(ctx, rel, Source{Kind: KindRecipe}, o)
		if err != nil {
			t.Fatal(err)
		}
		want := o.Getter.Path(rel.Whole.SHA256, "InstallESD.dmg")
		if calls.Load() != 1 || c.Path != want {
			t.Errorf("recipe calls %d, path %s; want 1 call and %s", calls.Load(), c.Path, want)
		}
		if c.Verified == nil || *c.Verified != (Delivered{Release: "10.9", Build: "13ZFIX", ContentDigest: rel.Whole.SHA256, Path: want}) {
			t.Errorf("Verified = %+v", c.Verified)
		}
	})

	t.Run("pinned file path", func(t *testing.T) {
		o, _ := sourceOptions(t)
		rel, calls := pinnedRelease(content, serve(t, 200, content))
		src, _ := writeFixture(t, "my-copy.dmg", content)
		c, err := Obtain(ctx, rel, Source{Kind: KindFile, Path: src}, o)
		if err != nil {
			t.Fatal(err)
		}
		if want := o.Getter.Path(rel.Whole.SHA256, "InstallESD.dmg"); c.Path != want {
			t.Errorf("imported to %s, want %s", c.Path, want)
		}
		if c.Verified == nil || c.Verified.Path != c.Path || c.Verified.Build != "13ZFIX" {
			t.Errorf("Verified = %+v", c.Verified)
		}
		if calls.Load() != 0 {
			t.Error("a file source ran the recipe")
		}
	})

	t.Run("pinned file URL", func(t *testing.T) {
		o, _ := sourceOptions(t)
		rel, _ := pinnedRelease(content, serve(t, 500, nil))
		media := serve(t, 200, content)
		c, err := Obtain(ctx, rel, Source{Kind: KindFile, URL: media.URL + "/download?id=7", AuthHeader: "Authorization: Bearer x"}, o)
		if err != nil {
			t.Fatal(err)
		}
		if want := o.Getter.Path(rel.Whole.SHA256, "InstallESD.dmg"); c.Path != want {
			t.Errorf("downloaded to %s, want %s", c.Path, want)
		}
		if media.lastAuth() != "Bearer x" {
			t.Errorf("the request's Authorization was %q, want %q", media.lastAuth(), "Bearer x")
		}
		if c.Verified == nil || c.Verified.ContentDigest != rel.Whole.SHA256 {
			t.Errorf("Verified = %+v", c.Verified)
		}
	})

	t.Run("pinned command", func(t *testing.T) {
		o, _ := sourceOptions(t)
		rel, _ := pinnedRelease(content, serve(t, 500, nil))
		src, _ := writeFixture(t, "made.dmg", content)
		script := writeScript(t, "make-esd", `cp "$1" "$out/whatever.bin"`)
		c, err := Obtain(ctx, rel, Source{Kind: KindCommand, Command: []string{script, src}}, o)
		if err != nil {
			t.Fatal(err)
		}
		if want := o.Getter.Path(rel.Whole.SHA256, "InstallESD.dmg"); c.Path != want {
			t.Errorf("imported to %s, want %s", c.Path, want)
		}
		if c.Verified == nil || c.Verified.Path != c.Path {
			t.Errorf("Verified = %+v", c.Verified)
		}
		if left := entries(t, o.WorkDir); len(left) != 0 {
			t.Errorf("the command's scratch is still in WorkDir after the import: %v", left)
		}
	})

	t.Run("unpinned file path", func(t *testing.T) {
		o, _ := sourceOptions(t)
		rel, disc := unpinnedDisc()
		path := writeImage(t, disc)
		c, err := Obtain(ctx, rel, Source{Kind: KindFile, Path: path}, o)
		if err != nil {
			t.Fatal(err)
		}
		if c.Path != path || c.Verified != nil {
			t.Errorf("Obtain = %+v, want the path in place, unverified", c)
		}
		if err := c.Discard(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Discard removed the user's own file: %v", err)
		}
	})

	t.Run("unpinned file URL", func(t *testing.T) {
		o, _ := sourceOptions(t)
		rel, disc := unpinnedDisc()
		media := serve(t, 200, disc)
		u := media.URL + "/disc?id=9"
		c, err := Obtain(ctx, rel, Source{Kind: KindFile, URL: u}, o)
		if err != nil {
			t.Fatal(err)
		}
		key := sha256.Sum256([]byte(u))
		if want := o.Getter.Path(hex.EncodeToString(key[:]), "install-disc.img"); c.Path != want {
			t.Errorf("downloaded to %s, want %s", c.Path, want)
		}
		if c.Verified == nil || *c.Verified != (Delivered{Release: "10.6", Build: "10ZFIX", ContentDigest: DiscDigest(rel.Discs[0]), Path: c.Path}) {
			t.Errorf("Verified = %+v", c.Verified)
		}
		// Again: from the cache, checked, with no request.
		before := media.requests.Load()
		again, err := Obtain(ctx, rel, Source{Kind: KindFile, URL: u}, o)
		if err != nil || again.Path != c.Path || again.Verified == nil || media.requests.Load() != before {
			t.Errorf("a second Obtain = %+v, %v, after %d more requests", again, err, media.requests.Load()-before)
		}
	})

	t.Run("unpinned command", func(t *testing.T) {
		o, _ := sourceOptions(t)
		rel, disc := unpinnedDisc()
		src := writeImage(t, disc)
		script := writeScript(t, "make-disc", `cp "$1" "$out/disc.iso"`)
		c, err := Obtain(ctx, rel, Source{Kind: KindCommand, Command: []string{script, src}}, o)
		if err != nil {
			t.Fatal(err)
		}
		rel2, err := filepath.Rel(o.WorkDir, c.Path)
		if err != nil || strings.HasPrefix(rel2, "..") || filepath.Base(c.Path) != "disc.iso" {
			t.Errorf("the command's file is at %s, want it in a scratch dir under %s", c.Path, o.WorkDir)
		}
		if c.Verified != nil {
			t.Errorf("Verified = %+v, want nil: Resolve checks it", c.Verified)
		}
		if _, err := Check(ctx, rel, c.Path); err != nil {
			t.Errorf("the command's file does not pass: %v", err)
		}
		if err := c.Discard(); err != nil {
			t.Fatal(err)
		}
		if left := entries(t, o.WorkDir); len(left) != 0 {
			t.Errorf("Discard left %v in WorkDir", left)
		}
	})

	t.Run("unpinned recipe", func(t *testing.T) {
		o, _ := sourceOptions(t)
		rel, _ := unpinnedDisc()
		_, err := Obtain(ctx, rel, Source{Kind: KindRecipe}, o)
		if err == nil || err.Error() != "release 10.6 has no built-in recipe: name a file or command source" {
			t.Errorf("Obtain(recipe) for a release with none = %v", err)
		}
	})
}

func TestResolveKeepsTheFirstThatPasses(t *testing.T) {
	content := []byte("pinned bytes")
	recipe := serve(t, 200, content)
	rel, calls := pinnedRelease(content, recipe)
	media := serve(t, 200, content)
	bad, _ := writeFixture(t, "bad.dmg", []byte("not the pinned bytes"))
	o, _ := sourceOptions(t)
	d, err := Resolve(context.Background(), rel, []Source{
		{Kind: KindFile, Path: bad},
		{Kind: KindFile, URL: media.URL + "/esd"},
		{Kind: KindRecipe},
	}, o)
	if err != nil {
		t.Fatal(err)
	}
	want := Delivered{Release: "10.9", Build: "13ZFIX", ContentDigest: rel.Whole.SHA256, Path: o.Getter.Path(rel.Whole.SHA256, "InstallESD.dmg")}
	if d != want {
		t.Errorf("Resolve = %+v, want %+v", d, want)
	}
	if media.requests.Load() != 1 || recipe.requests.Load() != 0 || calls.Load() != 0 {
		t.Errorf("the URL got %d requests and the recipe's server %d (recipe ran %d times); want 1, 0, 0",
			media.requests.Load(), recipe.requests.Load(), calls.Load())
	}
}

func TestResolveChecksAnUnverifiedCandidateAndDiscardsOneThatFails(t *testing.T) {
	rel, disc := unpinnedDisc()
	o, _ := sourceOptions(t)
	bad := writeScript(t, "bad-disc", `echo not a disc > "$out/disc.img"`)
	good := writeImage(t, disc)
	d, err := Resolve(context.Background(), rel, []Source{
		{Kind: KindCommand, Command: []string{bad}},
		{Kind: KindFile, Path: good},
	}, o)
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != good || d.Build != "10ZFIX" {
		t.Errorf("Resolve = %+v", d)
	}
	if left := entries(t, o.WorkDir); len(left) != 0 {
		t.Errorf("the failed command's scratch is still in WorkDir: %v", left)
	}
}

func TestResolveReportsEveryFailureInOrder(t *testing.T) {
	rel, _ := unpinnedDisc()
	o, logged := sourceOptions(t)
	gone := serve(t, 404, []byte("no"))
	notADisc, _ := writeFixture(t, "x.img", []byte("not a disc"))
	sources := []Source{
		{Kind: KindFile, Path: filepath.Join(t.TempDir(), "absent.img")},
		{Kind: KindFile, URL: gone.URL + "/disc.img"},
		{Kind: KindCommand, Command: []string{writeScript(t, "fails", "exit 1")}},
		{Kind: KindRecipe},
		{Kind: KindFile, Path: notADisc},
	}
	_, err := Resolve(context.Background(), rel, sources, o)
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("Resolve = %v, want a *ResolveError", err)
	}
	if re.Release != "10.6" || len(re.Tries) != len(sources) {
		t.Fatalf("ResolveError = %+v", re)
	}
	for i, s := range sources {
		if re.Tries[i].Source != s.String() || re.Tries[i].Err == nil {
			t.Errorf("try %d = %+v, want source %q with an error", i, re.Tries[i], s)
		}
		if !strings.Contains(err.Error(), s.String()+": "+re.Tries[i].Err.Error()) {
			t.Errorf("Resolve's error %q does not report try %d", err, i)
		}
	}
	for i, want := range []string{"absent.img", "HTTP 404", "status 1", "no built-in recipe", "x.img"} {
		if !strings.Contains(re.Tries[i].Err.Error(), want) {
			t.Errorf("try %d's error %q does not contain %q", i, re.Tries[i].Err, want)
		}
	}
	if len(logged.all()) == 0 {
		t.Error("Resolve logged nothing about the sources that failed")
	}
}

func TestResolveRefusesAnInvalidSourceBeforeTryingAny(t *testing.T) {
	content := []byte("pinned")
	recipe := serve(t, 200, content)
	rel, calls := pinnedRelease(content, recipe)
	o, _ := sourceOptions(t)
	_, err := Resolve(context.Background(), rel, []Source{{Kind: KindRecipe}, {Kind: "script", URL: "https://h/s3cr3t"}}, o)
	if err == nil || !strings.Contains(err.Error(), `"script"`) || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("Resolve = %v, want a refusal naming kind \"script\"", err)
	}
	if calls.Load() != 0 {
		t.Error("Resolve ran the recipe before refusing the second source")
	}
	if _, err := Resolve(context.Background(), rel, nil, o); err == nil {
		t.Error("Resolve with no sources succeeded")
	}
}

func TestResolveNeverPrintsAURLGivenAsAPath(t *testing.T) {
	rel, _ := unpinnedDisc()
	o, logged := sourceOptions(t)
	_, err := Resolve(context.Background(), rel, []Source{{Kind: KindFile, Path: "https://h.example/s3cr3t/disc.img?token=t0k"}}, o)
	if err == nil || !strings.Contains(err.Error(), "looks like a URL") {
		t.Fatalf("Resolve = %v, want the path refused as a URL", err)
	}
	for _, line := range append(logged.all(), err.Error()) {
		for _, secret := range []string{"s3cr3t", "t0k", "h.example"} {
			if strings.Contains(line, secret) {
				t.Errorf("%q carries %q", line, secret)
			}
		}
	}
}

// brokenReader yields what it holds, then fails as a pipe closed under it
// does.
type brokenReader struct{ data []byte }

func (r *brokenReader) Read(b []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errors.New("read |0: file already closed")
	}
	n := copy(b, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestCopyOutputLogsOnlyAWriteFailure(t *testing.T) {
	// A read that ends in an error -- the pipe closed once the wait for
	// leftovers ran out -- is no failure of the writer, and is not logged.
	l := &logs{}
	var got bytes.Buffer
	copyOutput(&got, &brokenReader{data: []byte("said")}, l.log)
	if got.String() != "said" || len(l.all()) != 0 {
		t.Errorf("a read failure: wrote %q and logged %q, want \"said\" and nothing", got.String(), l.all())
	}
	// A writer that fails is logged once, and the rest is drained.
	l = &logs{}
	src := bytes.NewReader(make([]byte, 1<<20))
	w := &failingWriter{}
	copyOutput(w, src, l.log)
	if src.Len() != 0 || w.writes.Load() != 1 {
		t.Errorf("a write failure left %d bytes unread after %d writes, want all drained after 1", src.Len(), w.writes.Load())
	}
	if all := l.all(); len(all) != 1 || !strings.Contains(all[0], "could not be written") || !strings.Contains(all[0], "the log is gone") {
		t.Errorf("a write failure logged %q", all)
	}
	// A writer that writes short with no error breaks io.Writer's
	// contract, and io.Copy stops at it: the rest is drained all the same.
	l = &logs{}
	src = bytes.NewReader(make([]byte, 1<<20))
	copyOutput(shortWriter{}, src, l.log)
	if src.Len() != 0 {
		t.Errorf("a short write left %d bytes unread, want all drained", src.Len())
	}
}

// shortWriter takes one byte of every write and says nothing is wrong.
type shortWriter struct{}

func (shortWriter) Write(b []byte) (int, error) { return min(len(b), 1), nil }

func TestResolveStopsWhenCancelled(t *testing.T) {
	rel, _ := unpinnedDisc()
	o, _ := sourceOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recipe := serve(t, 200, nil)
	_, err := Resolve(ctx, rel, []Source{{Kind: KindFile, URL: recipe.URL + "/a"}, {Kind: KindFile, URL: recipe.URL + "/b"}}, o)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Resolve under a cancelled context = %v, want context.Canceled", err)
	}
	var re *ResolveError
	if errors.As(err, &re) && len(re.Tries) != 1 {
		t.Errorf("Resolve went on past the cancel: %+v", re.Tries)
	}
	if recipe.requests.Load() != 0 {
		t.Errorf("Resolve made %d requests after a cancel", recipe.requests.Load())
	}
}

// offline is a client that dials only the loopback test servers, on
// 127.0.0.1 or ::1. Any other host fails as a lookup that found nothing,
// with exactly the error the resolver gives (which names the host), and
// no lookup made.
func offline() *http.Client {
	var d net.Dialer
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return d.DialContext(ctx, network, addr)
	}}
	return &http.Client{Transport: tr}
}

func TestResolveNeverPrintsASecret(t *testing.T) {
	secrets := []string{"s3cr3t", "t0k", "h3ad3r"}
	const header = "Authorization: Bearer h3ad3r"
	content := []byte("pinned bytes")

	// A server that echoes the request's URL and header everywhere it can:
	// the reason phrase, a redirect and the body.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.URL.String()+" "+r.Header.Get("Authorization"))
		switch {
		case strings.Contains(r.URL.Path, "/redirect"):
			http.Redirect(w, r, "/s3cr3t/gone?token=t0k", http.StatusFound)
		case strings.Contains(r.URL.Path, "/wrong"):
			w.Write([]byte("wrong bytes " + r.URL.String()))
		default:
			http.Error(w, "nope: "+r.URL.String()+" "+r.Header.Get("Authorization"), http.StatusForbidden)
		}
	}))
	defer echo.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	// Named apart from their URLs: a subtest's name is in its TempDir,
	// and so in every path the test's own cache puts in an error.
	urls := map[string]string{
		"forbidden":       echo.URL + "/s3cr3t?token=t0k",
		"redirected":      echo.URL + "/redirect/s3cr3t?token=t0k",
		"the wrong bytes": echo.URL + "/wrong/s3cr3t?token=t0k",
		"refused":         closedURL + "/s3cr3t?token=t0k",
		"no such host":    "http://s3cr3t.invalid/s3cr3t?token=t0k",
	}
	pinned, _ := pinnedRelease(content, serve(t, 500, nil))
	unpinned, _ := unpinnedDisc()
	for _, rel := range []Release{pinned, unpinned} {
		for name, u := range urls {
			t.Run(rel.Name+" "+name, func(t *testing.T) {
				o, logged := sourceOptions(t)
				o.Getter.Client = offline()
				_, err := Resolve(context.Background(), rel, []Source{{Kind: KindFile, URL: u, AuthHeader: header}}, o)
				if err == nil {
					t.Fatal("Resolve succeeded")
				}
				if name == "no such host" && !strings.Contains(err.Error(), "no such host") {
					t.Errorf("the error %q is not the failed lookup's", err)
				}
				if !strings.Contains(err.Error(), "file at a URL (withheld)") {
					t.Errorf("the error %q does not name the source as withheld", err)
				}
				for _, line := range append(logged.all(), err.Error()) {
					for _, s := range secrets {
						if strings.Contains(line, s) {
							t.Errorf("%q carries the secret %q", line, s)
						}
					}
				}
			})
		}
	}
}

func TestCommandSourceRules(t *testing.T) {
	ctx := context.Background()
	rel, disc := unpinnedDisc()
	good := writeImage(t, disc)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"one file", `cp "$1" "$out/disc.img"; chmod 755 "$out/disc.img"`, ""},
		{"nothing", `:`, "wrote no file"},
		{"two files", `cp "$1" "$out/a.img"; cp "$1" "$out/b.img"`, "wrote 2 files"},
		{"a dotfile beside the file", `cp "$1" "$out/a.img"; : > "$out/.DS_Store"`, "wrote 2 files"},
		{"a symlink", `ln -s "$1" "$out/disc.img"`, "symbolic link"},
		{"a directory", `mkdir "$out/disc.img"`, "not a regular file"},
		{"a hard link to someone else's file", `ln "$2" "$out/disc.img"`, "hard link"},
		{"exit 3 after a good file", `cp "$1" "$out/disc.img"; exit 3`, "exited with status 3"},
		{"killed by a signal", `cp "$1" "$out/disc.img"; kill -9 $$`, "signal"},
		{"a straggler", `cp "$1" "$out/disc.img"; (sleep 1; : > "$out/late") >/dev/null 2>&1 &`, ""},
		{"an unwritable scratch dir", `cp "$1" "$out/disc.img"; mkdir -p "$HOME/locked/in"; chmod 0 "$HOME/locked"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := sourceOptions(t)
			script := writeScript(t, "make-disc", tc.body)
			c, err := Obtain(ctx, rel, Source{Kind: KindCommand, Command: []string{script, good, victim}}, o)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("Obtain = %+v, %v, want an error containing %q", c, err, tc.want)
				}
				if left := entries(t, o.WorkDir); len(left) != 0 {
					t.Errorf("a refused command left %v in WorkDir", left)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m := modeOf(t, c.Path); m != 0o644 {
				t.Errorf("the command's file is mode %v, want 0644", m)
			}
			if left := entries(t, filepath.Dir(filepath.Dir(c.Path))); strings.Join(left, " ") != "out" {
				t.Errorf("the candidate's scratch holds %v, want only out: home and tmp go once the command is done", left)
			}
			if tc.name == "a straggler" {
				time.Sleep(1500 * time.Millisecond)
				if left := entries(t, filepath.Dir(c.Path)); len(left) != 1 {
					t.Errorf("a process the command left behind wrote after it exited: %v", left)
				}
			}
			if err := c.Discard(); err != nil {
				t.Fatal(err)
			}
			if left := entries(t, o.WorkDir); len(left) != 0 {
				t.Errorf("Discard left %v in WorkDir", left)
			}
		})
	}
	if m := modeOf(t, victim); m != 0o600 {
		t.Errorf("the hard-linked file's mode became %v", m)
	}
}

func TestTheCommandEnvironmentIsStripped(t *testing.T) {
	t.Setenv("LEAK", "1")
	rel, _ := unpinnedDisc()
	o, _ := sourceOptions(t)
	script := writeScript(t, "dump-env", `{ env; echo "LAST_ARGV=$out"; echo "CWD=$(pwd -P)"; } > "$out/env.txt"`)
	c, err := Obtain(context.Background(), rel, Source{Kind: KindCommand, Command: []string{script, "first"}}, o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Discard()
	b, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Dir(c.Path)
	scratch := filepath.Dir(out)
	var env, rest []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		name, _, _ := strings.Cut(line, "=")
		switch name {
		case "PWD", "OLDPWD", "SHLVL", "_":
			// The shell's own, not passed to it.
		case "LAST_ARGV", "CWD":
			rest = append(rest, line)
		default:
			env = append(env, line)
		}
	}
	sort.Strings(env)
	want := []string{"HOME=" + scratch + "/home", "LANG=C", "LONGBOAT_RELEASE=10.6", "PATH=/usr/bin:/bin", "TMPDIR=" + scratch + "/tmp"}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Errorf("the command's environment is\n%s\nwant\n%s", strings.Join(env, "\n"), strings.Join(want, "\n"))
	}
	realScratch, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"LAST_ARGV=" + out, "CWD=" + realScratch}; strings.Join(rest, "\n") != strings.Join(want, "\n") {
		t.Errorf("the command saw %q, want %q", rest, want)
	}
}

func TestTheCommandsOutputGoesToOutput(t *testing.T) {
	rel, disc := unpinnedDisc()
	o, _ := sourceOptions(t)
	var out bytes.Buffer
	o.Output = &out
	// The child it leaves behind holds the output open: Obtain must not
	// wait for it.
	script := writeScript(t, "talk", `echo to stdout; echo to stderr >&2; cp "$1" "$out/disc.img"; sleep 300 &`)
	start := time.Now()
	c, err := Obtain(context.Background(), rel, Source{Kind: KindCommand, Command: []string{script, writeImage(t, disc)}}, o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Discard()
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Obtain took %v: it waited on the child the command left behind", took)
	}
	if got := out.String(); got != "to stdout\nto stderr\n" {
		t.Errorf("Output got %q", got)
	}
}

// failingWriter fails every write.
type failingWriter struct{ writes atomic.Int32 }

func (w *failingWriter) Write(b []byte) (int, error) {
	w.writes.Add(1)
	return 0, errors.New("the log is gone")
}

func TestAFailingOutputDoesNotHangTheCommand(t *testing.T) {
	rel, disc := unpinnedDisc()
	o, logged := sourceOptions(t)
	w := &failingWriter{}
	o.Output = w
	// A megabyte of output: far more than a pipe holds.
	script := writeScript(t, "chatty", `dd if=/dev/zero bs=1024 count=1024 2>/dev/null; cp "$1" "$out/disc.img"`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	c, err := Obtain(ctx, rel, Source{Kind: KindCommand, Command: []string{script, writeImage(t, disc)}}, o)
	if err != nil {
		t.Fatalf("Obtain = %v after %v", err, time.Since(start))
	}
	defer c.Discard()
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Obtain took %v", took)
	}
	if w.writes.Load() != 1 {
		t.Errorf("the failing writer was written %d times, want once: after it fails the output is drained", w.writes.Load())
	}
	if !strings.Contains(strings.Join(logged.all(), "\n"), "the log is gone") {
		t.Errorf("the writer's failure was not logged: %q", logged.all())
	}
}

// alive reports whether pid is a process that has not exited: a zombie,
// reaped by nobody yet, has exited.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist) || runtime.GOOS != "linux"
	}
	// pid (comm) S ...: the state follows the last ")".
	s := string(stat)
	i := strings.LastIndex(s, ")")
	return i < 0 || i+2 >= len(s) || s[i+2] != 'Z'
}

func TestCancellingKillsTheCommandsProcessGroup(t *testing.T) {
	rel, _ := unpinnedDisc()
	o, _ := sourceOptions(t)
	pidDir := t.TempDir()
	script := writeScript(t, "hang", `sleep 300 &
echo $! > "$1/child.tmp" && mv "$1/child.tmp" "$1/child.pid"
wait`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		c   Candidate
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := Obtain(ctx, rel, Source{Kind: KindCommand, Command: []string{script, pidDir}}, o)
		done <- result{c, err}
	}()
	var pid int
	for deadline := time.Now().Add(10 * time.Second); ; {
		if b, err := os.ReadFile(filepath.Join(pidDir, "child.pid")); err == nil {
			if pid, err = strconv.Atoi(strings.TrimSpace(string(b))); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the command never wrote its child's pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Errorf("a cancelled Obtain = %+v, %v, want context.Canceled", r.c, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Obtain did not return within 5 s of the cancel")
	}
	t.Logf("Obtain returned %v after the cancel", time.Since(start))
	for deadline := time.Now().Add(5 * time.Second); alive(pid); {
		if time.Now().After(deadline) {
			if p, err := os.FindProcess(pid); err == nil {
				p.Kill()
			}
			t.Fatalf("the command's child %d outlived the cancel", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if left := entries(t, o.WorkDir); len(left) != 0 {
		t.Errorf("a cancelled command left %v in WorkDir", left)
	}
}

func TestNoKindFetchesCodeFromAURL(t *testing.T) {
	for _, s := range []Source{
		{Kind: "script", URL: "https://h/x.sh"},
		{Kind: "url", URL: "https://h/x.sh"},
		{Kind: "", URL: "https://h/x.sh"},
	} {
		err := s.Validate()
		if err == nil {
			t.Errorf("Validate accepted kind %q", s.Kind)
			continue
		}
		name := `"` + string(s.Kind) + `"`
		if s.Kind == "" {
			name = "kind is required"
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("Validate(kind %q) = %q, want it to name the kind", s.Kind, err)
		}
	}
	if err := (Source{Kind: KindCommand, Command: []string{"https://h/x.sh"}}).Validate(); err == nil {
		t.Error("Validate accepted a command whose program is a URL")
	}

	// Media that would leave a mark if it were ever run.
	marker := filepath.Join(t.TempDir(), "ran")
	payload := []byte("#!/bin/sh\n: > " + marker + "\n")
	srv := serve(t, 200, payload)
	rel, _ := pinnedRelease(payload, serve(t, 500, nil))
	exe, _ := writeFixture(t, "media.dmg", payload)
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	sources := []Source{
		{Kind: KindFile, URL: srv.URL + "/media"},
		{Kind: KindFile, Path: exe},
	}
	if runtime.GOOS != "windows" {
		sources = append(sources, Source{Kind: KindCommand, Command: []string{writeScript(t, "mk", `cp "$1" "$out/m"; chmod 755 "$out/m"`), exe}})
	}
	for _, s := range sources {
		o, _ := sourceOptions(t)
		c, err := Obtain(context.Background(), rel, s, o)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if m := modeOf(t, c.Path); m != 0o644 {
			t.Errorf("%s: the delivered file is mode %v, want 0644", s, m)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a delivered file was run")
	}

	// Unpinned (10.6): a URL is downloaded into the cache, so it is 0644
	// too. (The command case is TestCommandSourceRules's "one file".) A
	// user's own unpinned path is the one exception: it is used where it
	// is, and keeps the user's mode.
	disc, discBytes := unpinnedDisc()
	o, _ := sourceOptions(t)
	c, err := Obtain(context.Background(), disc, Source{Kind: KindFile, URL: serve(t, 200, discBytes).URL + "/disc"}, o)
	if err != nil {
		t.Fatal(err)
	}
	if m := modeOf(t, c.Path); m != 0o644 {
		t.Errorf("an unpinned URL's file is mode %v, want 0644", m)
	}
	mine := writeImage(t, discBytes)
	if err := os.Chmod(mine, 0o640); err != nil {
		t.Fatal(err)
	}
	if c, err := Obtain(context.Background(), disc, Source{Kind: KindFile, Path: mine}, o); err != nil || c.Path != mine {
		t.Fatalf("Obtain(%s) = %+v, %v", mine, c, err)
	}
	if m := modeOf(t, mine); m != 0o640 {
		t.Errorf("the user's own file became mode %v, want it left 0640", m)
	}
}
