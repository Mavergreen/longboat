package download

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// testGetter is a Getter whose cache is a temp directory, laid out as the
// plugin lays its own out: cache/<key>/<name>.
type testGetter struct {
	*Getter
	root string
}

func (g *testGetter) cache() string { return filepath.Join(g.root, "cache") }

func (g *testGetter) file(key, name string) string { return g.Path(key, name) }

func newGetter(t *testing.T, mod ...func(*Getter)) *testGetter {
	root := t.TempDir()
	g := &Getter{
		Path:    func(key, name string) string { return filepath.Join(root, "cache", key, name) },
		Backoff: time.Millisecond,
		Log:     func(string, ...any) {},
	}
	for _, m := range mod {
		m(g)
	}
	return &testGetter{Getter: g, root: root}
}

func getter(t *testing.T) *testGetter { return newGetter(t) }

func TestFilenameRules(t *testing.T) {
	for url, want := range map[string]string{
		"https://h/x/a.zip":       "a.zip",
		"https://h/x/a.zip?v=2":   "a.zip?v=2",
		"http://h/InstallESD.dmg": "InstallESD.dmg",
	} {
		if got, err := Filename(url); err != nil || got != want {
			t.Errorf("%s: %q %v", url, got, err)
		}
	}
	for _, bad := range []string{"https://h", "https://h/x/"} {
		if _, err := Filename(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestGetDownloadsVerifiesAndCaches(t *testing.T) {
	body := []byte("payload bytes")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(body)
	}))
	defer srv.Close()
	g := getter(t)
	it := Item{Name: "x", URL: srv.URL + "/d/x.zip", SHA256: sum(body)}
	p, err := g.Get(context.Background(), it)
	if err != nil || p != g.file(sum(body), "x.zip") {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := g.Get(context.Background(), it); err != nil || hits != 1 {
		t.Fatalf("second Get must use the cache: hits=%d err=%v", hits, err)
	}
}

func TestAMismatchedDownloadNeverTakesItsFinalName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("evil")) }))
	defer srv.Close()
	g := getter(t)
	want := sum([]byte("good"))
	_, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: want})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	// The datasource keys "the bytes at the URL moved" off this type.
	var mm *MismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("a download mismatch is a *MismatchError, got %T: %v", err, err)
	}
	if _, err := os.Stat(g.file(want, "x.zip")); !os.IsNotExist(err) {
		t.Fatal("an unverified download was renamed into place")
	}
}

// TestDownloadNeverWritesThroughAPreexistingPartName: a download written
// to a fixed name, dest+".part", would truncate whatever that name
// already pointed at -- a hard link to some other file, planted here --
// before a single byte was verified, corrupting everything else that
// shares the inode. This must never happen: every attempt creates its own
// uniquely named temp file and never opens a pre-existing path for
// writing.
func TestDownloadNeverWritesThroughAPreexistingPartName(t *testing.T) {
	g := getter(t)
	want := sum([]byte("good"))
	dest := g.file(want, "x.zip")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(original, []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixedPart := dest + ".part" // a fixed name this code must never touch
	if err := os.Link(original, fixedPart); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("evil")) }))
	defer srv.Close()

	if _, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: want}); err == nil {
		t.Fatal("a checksum mismatch must still be an error")
	}
	if b, err := os.ReadFile(original); err != nil || string(b) != "good" {
		t.Fatalf("the original, reachable through a pre-existing fixed .part link, was overwritten: %q %v", b, err)
	}
	if b, err := os.ReadFile(fixedPart); err != nil || string(b) != "good" {
		t.Fatalf("the fixed .part name's shared inode was overwritten: %q %v", b, err)
	}
}

// TestConcurrentGetsOfOneItemNeverProduceATornFile: with a fixed .part
// name, one goroutine's verify-then-rename could race a second goroutine
// still writing into what just became the first's dest, handing back a
// path whose bytes changed after Get verified them. Each attempt's own
// unique temp file rules this out.
func TestConcurrentGetsOfOneItemNeverProduceATornFile(t *testing.T) {
	good := []byte("the correct bytes, more than a few of them")
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.Write(good)
			return
		}
		w.Write([]byte("evil bytes of a different length"))
	}))
	defer srv.Close()
	g := getter(t)
	it := Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: sum(good)}

	var wg sync.WaitGroup
	paths := make([]string, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = g.Get(context.Background(), it)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for i, err := range errs {
		if err == nil {
			succeeded++
			b, rerr := os.ReadFile(paths[i])
			if rerr != nil || sum(b) != it.SHA256 {
				t.Fatalf("returned path's bytes do not verify: err=%v bytes=%q", rerr, b)
			}
		}
	}
	if succeeded == 0 {
		t.Fatal("the request that received the correct bytes must succeed")
	}
	dest := g.file(it.SHA256, "x.zip")
	if b, err := os.ReadFile(dest); err == nil && sum(b) != it.SHA256 {
		t.Fatalf("the cache file is torn or unverified: sum = %s", sum(b))
	}
}

func TestARotCachedFileIsAnErrorNotARedownload(t *testing.T) {
	g := getter(t)
	want := sum([]byte("good"))
	dest := g.file(want, "x.zip")
	os.MkdirAll(filepath.Dir(dest), 0o755)
	os.WriteFile(dest, []byte("rotted"), 0o644)
	_, err := g.Get(context.Background(), Item{Name: "x", URL: "http://127.0.0.1:1/x.zip", SHA256: want})
	if err == nil || !strings.Contains(err.Error(), dest) {
		t.Fatalf("err = %v", err)
	}
	// Rot in the cache is not "the URL's bytes moved".
	var mm *MismatchError
	if errors.As(err, &mm) {
		t.Fatalf("a corrupt cache entry must not be a *MismatchError: %v", err)
	}
}

func TestRetriesServerErrorsButNotClientErrors(t *testing.T) {
	body := []byte("ok")
	var n int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.Write(body)
	}))
	defer flaky.Close()
	g := getter(t)
	if _, err := g.Get(context.Background(), Item{Name: "f", URL: flaky.URL + "/f", SHA256: sum(body)}); err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var m int32
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m, 1)
		w.WriteHeader(404)
	}))
	defer gone.Close()
	_, err := g.Get(context.Background(), Item{Name: "g", URL: gone.URL + "/g", SHA256: sum(body)})
	if err == nil || m != 1 || !strings.Contains(err.Error(), "404") {
		t.Fatalf("m=%d err=%v", m, err)
	}
}

func TestHeadersAreSent(t *testing.T) {
	body := []byte("tok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "AssetToken=abc" {
			w.WriteHeader(403)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	g := getter(t)
	h := http.Header{}
	h.Set("Cookie", "AssetToken=abc")
	if _, err := g.Get(context.Background(), Item{Name: "t", URL: srv.URL + "/t", SHA256: sum(body), Header: h}); err != nil {
		t.Fatal(err)
	}
}

func TestNoFetchNeverContactsTheNetwork(t *testing.T) {
	var contacted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted.Store(true)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	g := getter(t)
	want := sum([]byte("good"))
	_, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: want, CacheOnly: true})
	if !errors.Is(err, ErrNotCached) {
		t.Fatalf("err = %v, want ErrNotCached", err)
	}
	if contacted.Load() {
		t.Fatal("CacheOnly must never contact the network")
	}
}

// TestPathTraversalInputsAreRejected: a checksum column like
// "../../../escaped" or a "https://h/x/.." URL must never reach
// Path/os.Create unvalidated.
func TestPathTraversalInputsAreRejected(t *testing.T) {
	g := getter(t)
	if _, err := g.Get(context.Background(), Item{Name: "x", URL: "http://h/x/a.zip", SHA256: "../../../escaped"}); err == nil || !strings.Contains(err.Error(), `"../../../escaped"`) {
		t.Fatalf("a non-hex checksum must be refused: %v", err)
	}
	if _, err := g.Get(context.Background(), Item{Name: "x", URL: "https://h/x/..", SHA256: sum([]byte("x"))}); err == nil || !strings.Contains(err.Error(), `".."`) {
		t.Fatalf("a \"..\" filename must be refused: %v", err)
	}
	if _, err := os.Stat(g.cache()); !os.IsNotExist(err) {
		t.Fatal("a rejected item must create nothing under the cache directory")
	}
}

func TestATruncatedBodyIsRetried(t *testing.T) {
	body := []byte("payload bytes, more than just a few of them")
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(200)
			w.Write(body[:len(body)/2]) // declared more than sent: io.ErrUnexpectedEOF
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	g := getter(t)
	p, err := g.Get(context.Background(), Item{Name: "t", URL: srv.URL + "/t", SHA256: sum(body)})
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(body) {
		t.Fatal("wrong bytes after the retry")
	}
}

func TestAStalledDownloadIsAbortedAndRetried(t *testing.T) {
	body := []byte("ok")
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.Header().Set("Content-Length", "1000000")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			time.Sleep(500 * time.Millisecond) // well past the test's stall timeout
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	g := newGetter(t, func(g *Getter) { g.StallTimeout = 50 * time.Millisecond })
	p, err := g.Get(context.Background(), Item{Name: "t", URL: srv.URL + "/t", SHA256: sum(body)})
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(body) {
		t.Fatal("wrong bytes after the stall retry")
	}
}

// TestNegativeStallTimeoutIsTreatedAsDefault: time.NewTimer treats a
// negative duration as "fire immediately", so a negative StallTimeout
// would otherwise abort every attempt as instantly stalled. stallDuration
// must default it exactly like 0.
func TestNegativeStallTimeoutIsTreatedAsDefault(t *testing.T) {
	g := &Getter{StallTimeout: -1}
	if got := g.stallDuration(); got != 2*time.Minute {
		t.Fatalf("stallDuration() = %v, want the 2-minute default", got)
	}
}

// TestANegativeStallTimeoutDoesNotAbortAnOrdinaryDownload: the behavioural
// version of the above -- a real (if slow-ish) download must still
// succeed on the first attempt, not be aborted and retried as if it had
// stalled.
func TestANegativeStallTimeoutDoesNotAbortAnOrdinaryDownload(t *testing.T) {
	body := []byte("ok, no stall here")
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.Write(body)
	}))
	defer srv.Close()
	g := newGetter(t, func(g *Getter) { g.StallTimeout = -1 })
	if _, err := g.Get(context.Background(), Item{Name: "t", URL: srv.URL + "/t", SHA256: sum(body)}); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// TestStaleTempsAreRemovedBeforeADownloadFreshOnesAreLeftAlone: a temp
// file left behind by a killed process (SIGKILL, OOM,
// power loss) is stale once it is older than the cleanup threshold and
// must be removed before the next attempt; one that is merely in
// progress (a fresh mtime) must be left alone.
func TestStaleTempsAreRemovedBeforeADownloadFreshOnesAreLeftAlone(t *testing.T) {
	g := getter(t)
	body := []byte("fresh download")
	dest := g.file(sum(body), "x.zip")
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-31 * time.Minute)

	staleTemp := filepath.Join(dir, ".x.zip.123456.part")
	if err := os.WriteFile(staleTemp, []byte("dead attempt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleTemp, old, old); err != nil {
		t.Fatal(err)
	}

	freshTemp := filepath.Join(dir, ".x.zip.654321.part")
	if err := os.WriteFile(freshTemp, []byte("in progress"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	if _, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: sum(body)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleTemp); !os.IsNotExist(err) {
		t.Fatal("a stale temp must be removed before a new download")
	}
	if _, err := os.Stat(freshTemp); err != nil {
		t.Fatal("a fresh temp must be left alone")
	}
}

// TestAFailedGetLeavesNoDirectoryItCreated: cache/<sha>/ (and cache/
// itself) are made only when something is about to be written into them,
// and a failure removes whatever this Get made, if it is still empty --
// a failed fetch must not leave a skeleton behind. A directory that was
// already there is left alone.
func TestAFailedGetLeavesNoDirectoryItCreated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("evil")) }))
	defer srv.Close()
	g := getter(t)
	want := sum([]byte("good"))
	if _, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: want}); err == nil {
		t.Fatal("a checksum mismatch must be an error")
	}
	if _, err := os.Stat(g.cache()); !os.IsNotExist(err) {
		t.Fatalf("a failed download left %s behind (%v)", g.cache(), err)
	}

	// CacheOnly with nothing cached never creates anything at all.
	if _, err := g.Get(context.Background(), Item{Name: "x", SHA256: want, Filename: "x.zip", CacheOnly: true}); !errors.Is(err, ErrNotCached) {
		t.Fatalf("err = %v, want ErrNotCached", err)
	}
	if _, err := os.Stat(g.cache()); !os.IsNotExist(err) {
		t.Fatalf("a cache-only Get left %s behind (%v)", g.cache(), err)
	}

	// A directory that already existed stays, empty or not.
	dir := filepath.Dir(g.file(want, "x.zip"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: want}); err == nil {
		t.Fatal("a checksum mismatch must be an error")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a directory Get did not create was removed: %v", err)
	}
}

// TestCacheFileModes: what Get writes itself -- a download -- is 0644 whatever the umask.
func TestCacheFileModes(t *testing.T) {
	body := []byte("payload bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	g := getter(t)
	p, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: sum(body)})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("a download's mode is %v (%v), want 0644", fi.Mode().Perm(), err)
	}
}

// TestRemoveEmptyNeverUnlinksAFile: removeEmpty takes back directories a
// failed Get made. If something has since put a file where one of them
// was, that file is not removeEmpty's to remove, whatever the list says.
func TestRemoveEmptyNeverUnlinksAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeEmpty([]string{f})
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("removeEmpty unlinked a file: %v", err)
	}
}

// TestATempSurvivesItsDirectoryVanishing: between Get making cache/<sha>/
// and creating its temp there, a concurrent caller whose own Get failed
// may remove the directory (it was empty, and that Get made it too).
// Creating the temp then fails with ENOENT; the directory is made again,
// once, and the temp created -- and the remade directory is Get's to
// take back if it fails.
func TestATempSurvivesItsDirectoryVanishing(t *testing.T) {
	d := &cacheDir{path: filepath.Join(t.TempDir(), "cache", "sha")}
	if err := d.prepare(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(d.path); err != nil {
		t.Fatal(err)
	}
	f, err := inCacheDir(d, func() (*os.File, error) { return os.CreateTemp(d.path, ".x.*.part") })
	if err != nil {
		t.Fatalf("the temp was not created after its directory vanished: %v", err)
	}
	f.Close()
	os.Remove(f.Name())
	d.cleanup()
	if _, err := os.Stat(d.path); !os.IsNotExist(err) {
		t.Fatalf("the remade directory was not taken back: %v", err)
	}
}

func unpinned(name string, check func(context.Context, string) error) Item {
	return Item{Name: "u", Key: sum([]byte(name)), Filename: name, Check: check}
}

func TestAnItemNeedsExactlyOnePinOrCheck(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("x"))
	}))
	defer srv.Close()
	g := getter(t)
	ok := func(context.Context, string) error { return nil }
	for name, it := range map[string]Item{
		"neither":                 {Name: "x", URL: srv.URL + "/x.zip"},
		"both":                    {Name: "x", URL: srv.URL + "/x.zip", SHA256: sum([]byte("x")), Key: sum([]byte("x")), Check: ok},
		"pin and check":           {Name: "x", URL: srv.URL + "/x.zip", SHA256: sum([]byte("x")), Check: ok},
		"pin and key":             {Name: "x", URL: srv.URL + "/x.zip", SHA256: sum([]byte("x")), Key: sum([]byte("x"))},
		"check without key":       {Name: "x", URL: srv.URL + "/x.zip", Check: ok},
		"key without check":       {Name: "x", URL: srv.URL + "/x.zip", Key: sum([]byte("x"))},
		"a key that escapes":      {Name: "x", URL: srv.URL + "/x.zip", Key: "../../escaped", Check: ok},
		"a secret w/o a filename": {Name: "x", URL: srv.URL + "/x.zip", SHA256: sum([]byte("x")), Secret: true},
		"a secret w/o a name":     {URL: srv.URL + "/x.zip", SHA256: sum([]byte("x")), Secret: true, Filename: "m"},
	} {
		if _, err := g.Get(context.Background(), it); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d request(s) before the item was refused", hits.Load())
	}
	if _, err := os.Stat(g.cache()); !os.IsNotExist(err) {
		t.Fatal("a refused item created something under the cache")
	}
}

func TestAnUnpinnedItemIsCheckedBeforeItTakesItsName(t *testing.T) {
	body := []byte("an unpinned body")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	g := getter(t)

	var seen string
	it := unpinned("d.dmg", func(_ context.Context, p string) error {
		seen = p
		return errors.New("not a disc")
	})
	it.URL = srv.URL + "/d.dmg"
	_, err := g.Get(context.Background(), it)
	if err == nil || !strings.Contains(err.Error(), "not a disc") {
		t.Fatalf("err = %v", err)
	}
	if seen == "" || seen == g.file(it.Key, "d.dmg") {
		t.Fatalf("Check saw %q: it must see the temp, not the final name", seen)
	}
	if _, err := os.Stat(g.file(it.Key, "d.dmg")); !os.IsNotExist(err) {
		t.Fatal("a file that failed its check took its name")
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatal("the temp of a failed check was left behind")
	}
	if _, err := os.Stat(g.cache()); !os.IsNotExist(err) {
		t.Fatal("a failed check left directories behind")
	}

	it.Check = func(_ context.Context, p string) error {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != string(body) {
			return fmt.Errorf("wrong bytes %q %v", b, err)
		}
		return nil
	}
	p, err := g.Get(context.Background(), it)
	if err != nil || p != g.file(it.Key, "d.dmg") {
		t.Fatalf("%q %v", p, err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v %v, want 0644", fi, err)
	}
}

func TestAnUnpinnedCachedFileIsRecheckedNotRefetched(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	g := getter(t)
	var calls int
	it := unpinned("d.dmg", func(context.Context, string) error { calls++; return nil })
	it.URL = srv.URL + "/d.dmg"
	dest := g.file(it.Key, "d.dmg")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("already here"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := g.Get(context.Background(), it)
	if err != nil || p != dest || calls != 1 || hits.Load() != 0 {
		t.Fatalf("p=%q err=%v calls=%d hits=%d", p, err, calls, hits.Load())
	}
	// A cached file that now fails its check is an error, not a refetch.
	it.Check = func(context.Context, string) error { return errors.New("rotted") }
	if _, err := g.Get(context.Background(), it); err == nil || !strings.Contains(err.Error(), "rotted") || hits.Load() != 0 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func TestImportVerifiesAndPlaces(t *testing.T) {
	body := []byte("a local file")
	src := filepath.Join(t.TempDir(), "InstallESD.dmg")
	if err := os.WriteFile(src, body, 0o600); err != nil {
		t.Fatal(err)
	}
	g := getter(t)
	it := Item{Name: "esd", SHA256: sum(body)}
	p, err := g.Import(context.Background(), it, src)
	if err != nil || p != g.file(sum(body), "InstallESD.dmg") {
		t.Fatalf("%q %v", p, err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v %v, want 0644", fi, err)
	}
	if b, _ := os.ReadFile(src); string(b) != string(body) {
		t.Fatal("the source was touched")
	}

	// Already present: no copy, so even a source that has gone is fine.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if p2, err := g.Import(context.Background(), it, src); err != nil || p2 != p {
		t.Fatalf("%q %v", p2, err)
	}

	// A wrong source: a MismatchError, nothing placed, nothing left.
	bad := filepath.Join(t.TempDir(), "bad.dmg")
	if err := os.WriteFile(bad, []byte("not it"), 0o644); err != nil {
		t.Fatal(err)
	}
	g2 := getter(t)
	_, err = g2.Import(context.Background(), it, bad)
	var mm *MismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %T %v", err, err)
	}
	if _, err := os.Stat(g2.cache()); !os.IsNotExist(err) {
		t.Fatal("a mismatched import left something under the cache")
	}

	// An unpinned import is checked like a download.
	g3 := getter(t)
	u := unpinned("u.dmg", func(context.Context, string) error { return errors.New("nope") })
	if _, err := g3.Import(context.Background(), u, bad); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(g3.cache()); !os.IsNotExist(err) {
		t.Fatal("a failed-check import left something under the cache")
	}
	u.Check = func(context.Context, string) error { return nil }
	if p, err := g3.Import(context.Background(), u, bad); err != nil || p != g3.file(u.Key, "u.dmg") {
		t.Fatalf("%q %v", p, err)
	}

	// A missing source is an error and creates nothing.
	g4 := getter(t)
	if _, err := g4.Import(context.Background(), it, filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("a missing source was accepted")
	}
	if _, err := os.Stat(g4.cache()); !os.IsNotExist(err) {
		t.Fatal("a missing source left something under the cache")
	}
}

func TestASecretItemNeverNamesItsURL(t *testing.T) {
	const path = "/s3cr3t?token=t0k"
	forbidden := []string{"s3cr3t", "t0k", "h3ad3r", "127.0.0.1"}
	hdr := http.Header{"Authorization": {"Bearer h3ad3r"}}
	body := []byte("the body")
	var mode atomic.Value // "404", "503", "stall", "ok"
	mode.Store("ok")
	var deadURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case "404":
			w.WriteHeader(404)
		case "503":
			w.WriteHeader(503)
		case "redirect":
			http.Redirect(w, r, deadURL+"/t0k-redirect", http.StatusFound)
		case "stall":
			w.Header().Set("Content-Length", "1000000")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			time.Sleep(300 * time.Millisecond)
		default:
			w.Write(body)
		}
	}))
	defer srv.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL = dead.URL
	dead.Close()
	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0) // the client rejects the cert on purpose
	tlsSrv.StartTLS()
	defer tlsSrv.Close()

	for name, c := range map[string]struct {
		mode  string
		url   string
		sha   string
		check func(context.Context, string) error
	}{
		"404":                {"404", srv.URL, sum(body), nil},
		"503 retried":        {"503", srv.URL, sum(body), nil},
		"connection refused": {"ok", deadURL, sum(body), nil},
		"a stall":            {"stall", srv.URL, sum(body), nil},
		"a mismatch":         {"ok", srv.URL, sum([]byte("other")), nil},
		"a check failure":    {"ok", srv.URL, "", func(context.Context, string) error { return errors.New("not a disc") }},
		"a redirect":         {"redirect", srv.URL, sum(body), nil},
		"an untrusted cert":  {"ok", tlsSrv.URL, sum(body), nil},
		"a malformed URL":    {"ok", "http://127.0.0.1:99999" + path + "\x7f", sum(body), nil},
		"no scheme":          {"ok", "127.0.0.1" + path, sum(body), nil},
	} {
		t.Run(name, func(t *testing.T) {
			mode.Store(c.mode)
			var logs []string
			g := newGetter(t, func(g *Getter) {
				g.Retries = 2
				g.StallTimeout = 50 * time.Millisecond
				g.Log = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
			})
			it := Item{Name: "file at a URL (withheld)", URL: c.url + path, Header: hdr, Secret: true, Filename: "media.dmg"}
			if c.check != nil {
				it.Key, it.Check = sum([]byte("k")), c.check
			} else {
				it.SHA256 = c.sha
			}
			_, err := g.Get(context.Background(), it)
			if err == nil {
				t.Fatal("no error")
			}
			all := append([]string{err.Error()}, logs...)
			for _, s := range all {
				for _, f := range forbidden {
					if strings.Contains(s, f) {
						t.Errorf("%q leaks %q", s, f)
					}
				}
			}
			if !strings.Contains(err.Error(), it.Name) {
				t.Errorf("err = %q, want it to name %q", err, it.Name)
			}
			var se *StatusError
			if errors.As(err, &se) && se.URL != "" {
				t.Errorf("a secret StatusError carries URL %q", se.URL)
			}
			var ue *url.Error
			if errors.As(err, &ue) {
				t.Errorf("a *url.Error survives in the chain: %v", ue)
			}
		})
	}
}

func TestASecretCancelledGetNamesNoURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 1<<16))
		w.(http.Flusher).Flush()
		cancel()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	g := getter(t)
	_, err := g.Get(ctx, Item{Name: "n", URL: srv.URL + "/s3cr3t", SHA256: sum([]byte("g")), Secret: true, Filename: "m"})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchCapsTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/small":
			w.Write([]byte("0123456789"))
		case "/big":
			w.Write(make([]byte, 100))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	g := getter(t)
	if b, err := g.Fetch(context.Background(), srv.URL+"/small", 10); err != nil || string(b) != "0123456789" {
		t.Fatalf("exactly the cap must pass: %q %v", b, err)
	}
	_, err := g.Fetch(context.Background(), srv.URL+"/big", 10)
	if err == nil || !strings.Contains(err.Error(), "refusing to buffer more of what should be a small text file") {
		t.Fatalf("err = %v", err)
	}
	_, err = g.Fetch(context.Background(), srv.URL+"/nope", 10)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 || se.URL == "" {
		t.Fatalf("err = %T %v", err, err)
	}
}

func TestRetryAndTransient(t *testing.T) {
	n := 0
	err := Retry(context.Background(), 2, time.Millisecond, nil, "op", func() (bool, error) {
		n++
		return true, errors.New("again")
	})
	if err == nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !Transient(ctx, errors.New("connection reset")) {
		t.Fatal("a plain network error is transient")
	}
	cancel()
	if Transient(ctx, errors.New("connection reset")) {
		t.Fatal("nothing is transient once the context is done")
	}
}

// TestASecretStatusNeverEchoesTheServersReasonPhrase: net/http keeps the
// status line's reason phrase verbatim, and a server (or a proxy) that
// knows the URL can put it there.
func TestASecretStatusNeverEchoesTheServersReasonPhrase(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, "HTTP/1.1 404 /s3cr3t?token=t0k h3ad3r\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			}()
		}
	}()
	g := getter(t)
	_, err = g.Get(context.Background(), Item{Name: "n", URL: "http://" + ln.Addr().String() + "/s3cr3t?token=t0k", SHA256: sum([]byte("g")), Secret: true, Filename: "m"})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 {
		t.Fatalf("err = %T %v", err, err)
	}
	for _, f := range []string{"s3cr3t", "t0k", "h3ad3r", "127.0.0.1"} {
		if strings.Contains(err.Error(), f) {
			t.Errorf("%q leaks %q", err, f)
		}
	}
	if !strings.Contains(err.Error(), "404 Not Found") {
		t.Errorf("err = %q, want the standard status text", err)
	}
}
