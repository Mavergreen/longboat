// Package download fetches install media and its small companions,
// verified. One primitive, Getter.Get, does every download; Import takes a
// local file through the same verification. An item is either pinned to a
// whole-file SHA-256 or unpinned, in which case its own Check decides.
//
// The package imports nothing outside the standard library: where an item
// lives is the caller's, through Getter.Path.
package download

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type Getter struct {
	Client  *http.Client                      // nil: http.DefaultClient
	Path    func(key, filename string) string // required: where an item with this key lives
	Log     func(format string, a ...any)
	Retries int           // attempts after the first; 0 means 3, negative means 0
	Backoff time.Duration // first retry's wait, doubling; 0 (or negative) means 1s

	// StallTimeout aborts (and retries) an attempt that has read no body
	// bytes for this long: a captive portal or a stuck connection can
	// hold a socket open without ever failing it outright. 0 means 2
	// minutes.
	StallTimeout time.Duration
}

// Item is one thing to get. Exactly one of SHA256 (a whole-file pin) or
// Check together with Key (an unpinned item, content-checked) is set.
type Item struct {
	Name     string // for messages
	URL      string
	SHA256   string // the pin; also the cache key
	Key      string // an unpinned item's cache key: 64 lowercase hex
	Filename string // "" derives it from URL (or, for Import, from the source)
	Header   http.Header

	// Check decides whether an unpinned item is what it should be. Get
	// runs it on the finished temp file, before the file takes its name,
	// and again on a file already cached. It must not modify the file.
	// Its error is reported as is (wrapped with the item's name), so for
	// a Secret item it must not carry the URL or Header.
	Check func(ctx context.Context, path string) error

	// CacheOnly makes Get check the cache only, never the network:
	// ErrNotCached if the cache does not have it.
	CacheOnly bool

	// Secret says URL and Header are secrets: the library's own errors
	// and log lines never carry them, and name the item by Name instead
	// (only Check's error is reported as is, see Check), and
	// Filename (and Name) must be given, since the URL cannot name the
	// file.
	Secret bool
}

// MismatchError is Get's error when freshly downloaded (or imported)
// bytes do not match the pinned checksum: the file is not the one that
// was pinned. (A corrupt cache entry is a different, untyped error.)
type MismatchError struct{ msg string }

func (e *MismatchError) Error() string { return e.msg }

// StatusError is a response that was not a success. URL is empty for a
// Secret item, whose errors name it by Name instead.
type StatusError struct {
	URL, Status string
	Code        int
}

func (e *StatusError) Error() string {
	if e.URL == "" {
		return "HTTP " + e.Status
	}
	return e.URL + ": HTTP " + e.Status
}

// ErrNotCached is Get's result with Item.CacheOnly set, when nothing
// cached satisfies the item.
var ErrNotCached = errors.New("not in the cache")

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateSHA256 refuses anything that is not exactly 64 lowercase hex
// characters: the key is joined straight into a path, so a checksum
// column holding something like "../../../escaped" would otherwise escape
// the cache directory.
func ValidateSHA256(s string) error {
	if !sha256Hex.MatchString(s) {
		return fmt.Errorf("refusing checksum %q: want 64 lowercase hex characters", s)
	}
	return nil
}

// ValidateFilename refuses a name that would not stay inside the cache
// directory it is joined into: empty, ".", "..", or containing a path
// separator. A URL like https://h/x/.. derives exactly ".." as its last
// path segment, and Filename does not itself reject that.
func ValidateFilename(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("refusing filename %q", name)
	}
	if strings.ContainsRune(name, '/') || (os.PathSeparator != '/' && strings.ContainsRune(name, os.PathSeparator)) {
		return fmt.Errorf("refusing filename %q: contains a path separator", name)
	}
	return nil
}

// Filename is the URL's last path segment, query string and all.
func Filename(url string) (string, error) {
	i := strings.Index(url, "://")
	if i < 0 || !strings.Contains(url[i+3:], "/") {
		return "", fmt.Errorf("cannot derive a filename from %s -- it has no path; add one to the registry entry", url)
	}
	name := url[strings.LastIndex(url, "/")+1:]
	if name == "" {
		return "", fmt.Errorf("cannot derive a filename from %s -- it ends in \"/\"", url)
	}
	return name, nil
}

// ref is how messages name an item: its URL, unless that is a secret.
func (it Item) ref() string {
	if it.Secret {
		return it.Name
	}
	return it.URL
}

// key is the item's cache key, once validated.
func (it Item) key() string {
	if it.SHA256 != "" {
		return it.SHA256
	}
	return it.Key
}

// plan validates the item and returns its cache key and filename. deriveName
// supplies the filename when the item gives none.
func (g *Getter) plan(it Item, deriveName func() (string, error)) (key, name string, err error) {
	if g.Path == nil {
		return "", "", errors.New("download: Getter.Path is required")
	}
	pinned, unpinned := it.SHA256 != "", it.Check != nil || it.Key != ""
	switch {
	case pinned && unpinned:
		return "", "", fmt.Errorf("%s: an item is pinned by SHA256 or checked by Check and Key, not both", it.Name)
	case !pinned && (it.Check == nil || it.Key == ""):
		return "", "", fmt.Errorf("%s: an item needs a SHA256, or both a Check and a Key", it.Name)
	}
	if it.Secret && (it.Name == "" || it.Filename == "") {
		return "", "", errors.New("a Secret item needs a Name and a Filename, since its URL may not name either")
	}
	name = it.Filename
	if name == "" {
		if name, err = deriveName(); err != nil {
			return "", "", err
		}
	}
	if err := ValidateFilename(name); err != nil {
		return "", "", err
	}
	key = it.key()
	if err := ValidateSHA256(key); err != nil {
		return "", "", err
	}
	return key, name, nil
}

// verifyCached holds a file already in the cache to the item's pin or
// Check.
func verifyCached(ctx context.Context, it Item, dest string) error {
	if it.SHA256 != "" {
		got, err := SHA256File(ctx, dest)
		if err != nil {
			return err
		}
		if got != it.SHA256 {
			return fmt.Errorf("checksum mismatch for %s: want %s, got %s (remove it to fetch again)", dest, it.SHA256, got)
		}
		return nil
	}
	if err := it.Check(ctx, dest); err != nil {
		return fmt.Errorf("%s fails its check: %w (remove it to fetch again)", dest, err)
	}
	return nil
}

// Get returns a verified path to it: from the cache, else downloaded.
//
// A returned path is read-only -- never open it for writing, truncate it
// or change its mode, here or in any caller: it is the cache's verified
// copy. Every write happens on a freshly created, uniquely named file,
// verified in full, made 0644, then renamed into place; nothing
// unverified is ever renamed to its final name.
//
// Get creates the item's directory (and any missing parent) only when it
// is about to write a download's temp into it, and removes every
// directory it created, if still empty, when it fails: a failed fetch
// leaves nothing behind, not even an empty home that would look like one
// in use.
//
// A done ctx (a signal) stops Get with ctx's error wherever it is: before
// it starts, every few MiB of hashing (ctxReader), and mid-download. A
// cancelled Get never reports success, and removes whatever temp it had
// made.
func (g *Getter) Get(ctx context.Context, it Item) (path string, err error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%s: %w", it.Name, err)
	}
	key, name, err := g.plan(it, func() (string, error) {
		if it.Secret {
			return "", errors.New("unreachable") // plan refuses a Secret item without a Filename
		}
		return Filename(it.URL)
	})
	if err != nil {
		return "", err
	}
	dest := g.Path(key, name)
	if found, err := g.cached(ctx, it, dest); found || err != nil {
		return dest, err
	}

	cd := &cacheDir{path: filepath.Dir(dest)}
	defer func() {
		if err != nil {
			cd.cleanup()
		}
	}()

	if it.CacheOnly {
		return "", ErrNotCached
	}
	if err := cd.prepare(); err != nil {
		return "", err
	}
	if err := g.download(ctx, it, cd, name, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// cached reports whether dest is already there, verifying it if so.
func (g *Getter) cached(ctx context.Context, it Item, dest string) (bool, error) {
	if _, err := os.Stat(dest); err == nil {
		return true, verifyCached(ctx, it, dest)
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %s: %w", dest, err)
	}
	return false, nil
}

// Import places a local file, src, as the item's file, verified exactly
// as a download is: copied to a temp of its own, held to the pin (a
// *MismatchError if it fails) or to Check, made 0644 and renamed into
// place. src is only read. An item already in the cache is verified and
// returned without copying. The item's URL, Header and CacheOnly are
// ignored; an empty Filename is src's base name.
func (g *Getter) Import(ctx context.Context, it Item, src string) (path string, err error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%s: %w", it.Name, err)
	}
	key, name, err := g.plan(it, func() (string, error) { return filepath.Base(src), nil })
	if err != nil {
		return "", err
	}
	dest := g.Path(key, name)
	if found, err := g.cached(ctx, it, dest); found || err != nil {
		return dest, err
	}
	// Stat before open: open(2) on a FIFO with no writer blocks, and a
	// context cannot interrupt it. The Stat after is for a swap in between.
	if fi, err := os.Stat(src); err != nil {
		return "", err
	} else if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil {
		return "", err
	} else if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", src)
	}

	cd := &cacheDir{path: filepath.Dir(dest)}
	defer func() {
		if err != nil {
			cd.cleanup()
		}
	}()
	if err := cd.prepare(); err != nil {
		return "", err
	}
	_, err = g.stage(ctx, it, cd, name, dest, src, func(w io.Writer) (bool, error) {
		_, err := io.Copy(w, &ctxReader{ctx: ctx, r: in})
		return false, err
	})
	if err != nil {
		return "", err
	}
	return dest, nil
}

// cacheDir is the directory Get places an item in, made only when
// something is about to be written there, and remembering which
// directories it made so a failed Get can take exactly those back.
type cacheDir struct {
	path    string
	ready   bool
	created []string // deepest first
}

// prepare makes the directory, and any missing parent, the first time it
// is called.
func (d *cacheDir) prepare() error {
	if d.ready {
		return nil
	}
	if err := d.remake(); err != nil {
		return err
	}
	d.ready = true
	return nil
}

// remake makes the directory and any missing parent, recording the ones
// it made ahead of those made before.
func (d *cacheDir) remake() error {
	c, err := mkdirs(d.path)
	if err != nil {
		return err
	}
	d.created = append(c, d.created...)
	return nil
}

// cleanup removes, deepest first, every directory d made that is empty.
func (d *cacheDir) cleanup() { removeEmpty(d.created) }

// inCacheDir runs create, which makes a temp inside d. A concurrent
// caller whose own Get failed may have removed d between prepare and
// create -- it was empty, and that Get had made it too -- so an ENOENT
// from create makes d again, once, and tries once more.
func inCacheDir[T any](d *cacheDir, create func() (T, error)) (T, error) {
	v, err := create()
	if errors.Is(err, fs.ErrNotExist) {
		if rerr := d.remake(); rerr != nil {
			return v, err
		}
		v, err = create()
	}
	return v, err
}

// mkdirs is os.MkdirAll(dir), returning the directories it had to create,
// deepest first, so a caller that fails can take back exactly those.
func mkdirs(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; {
		if _, err := os.Lstat(d); err == nil || !os.IsNotExist(err) {
			break
		}
		missing = append(missing, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		removeEmpty(missing)
		return nil, err
	}
	return missing, nil
}

// removeEmpty removes each of dirs, in order, that is an empty directory.
// It uses rmdir(2) itself, never os.Remove, which would unlink a file
// that had somehow taken one of those names; rmdir refuses a file and a
// non-empty directory alike, so a directory a concurrent caller has since
// put something in stays, as does everything above it.
func removeEmpty(dirs []string) {
	for _, d := range dirs {
		syscall.Rmdir(d)
	}
}

// download tries the network, retrying failures worth retrying. Each
// attempt owns a uniquely named temp file it creates itself and removes on
// any failure: no fixed ".part" name is ever used, so a second run, a
// crash mid-download, or a file an earlier attempt left behind can never
// make this attempt write through a name that already points somewhere
// else (an earlier crash's leftover, or a hard link someone planted).
func (g *Getter) download(ctx context.Context, it Item, cd *cacheDir, name, dest string) error {
	g.cleanStaleTemps(it, cd.path, name)
	return g.retryPolicy().do(ctx, it.Name, func() (bool, error) {
		return g.attempt(ctx, it, cd, name, dest)
	})
}

// retryPolicy is g's Retries and Backoff, defaulted.
func (g *Getter) retryPolicy() retryPolicy { return newRetryPolicy(g.Retries, g.Backoff, g.logf) }

func (g *Getter) client() *http.Client {
	if g.Client == nil {
		return http.DefaultClient
	}
	return g.Client
}

// errTooManyRedirects is a redirect chain given up on. It is the whole
// reason reported for a Secret item, whose URL the chain must not name.
var errTooManyRedirects = errors.New("stopped after 10 redirects")

// clientFor is the client for it's download: g's own, except that a
// redirect that leaves the first URL's origin -- another host or port, or
// from https to plain http -- is followed without it.Header, as is every
// later hop, even one back to the first host. Go itself strips only
// Authorization and Cookie, and only on a hop to another domain, so a
// header like X-Api-Key would otherwise go wherever the server pointed.
//
// A Secret item's redirects also carry no Referer: Go sets it to the
// previous hop's whole URL, query and all, which would hand the secret to
// whatever server the redirect names. A non-secret item keeps Go's own.
func (g *Getter) clientFor(it Item) *http.Client {
	base := g.client()
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if it.Secret {
			req.Header.Del("Referer")
		}
		for _, hop := range append(via[1:len(via):len(via)], req) {
			if leavesOrigin(via[0].URL, hop.URL) {
				for k := range it.Header {
					delete(req.Header, k)
					req.Header.Del(k)
				}
				break
			}
		}
		if base.CheckRedirect != nil {
			return base.CheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return errTooManyRedirects
		}
		return nil
	}
	return &c
}

// leavesOrigin reports whether a redirect from from to to goes to
// another host or port, or from https down to http. An upgrade from http
// to https on the same host, each on its scheme's default port, does not.
func leavesOrigin(from, to *url.URL) bool {
	if strings.EqualFold(from.Scheme, "https") && !strings.EqualFold(to.Scheme, "https") {
		return true
	}
	if !strings.EqualFold(from.Hostname(), to.Hostname()) {
		return true
	}
	return from.Port() != to.Port() && !(isDefaultPort(from) && isDefaultPort(to))
}

// isDefaultPort reports whether u names no port, or its scheme's own.
func isDefaultPort(u *url.URL) bool {
	switch p := u.Port(); {
	case p == "":
		return true
	case strings.EqualFold(u.Scheme, "http"):
		return p == "80"
	case strings.EqualFold(u.Scheme, "https"):
		return p == "443"
	}
	return false
}

// request builds the GET for rawURL. A malformed URL's own error embeds
// it, so a secret one is reported as just that.
func newRequest(ctx context.Context, rawURL string, header http.Header, ref string, secret bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		if secret {
			return nil, fmt.Errorf("%s: the URL is not valid", ref)
		}
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req, nil
}

// attempt is one full download try: a unique temp file, hashed while it is
// written, verified and renamed into place on success. It removes its own
// temp file on every failure (network error, stall, or checksum mismatch)
// -- nothing is kept "for inspection": a unique name is never reused, so
// there is nothing a next attempt could confuse for its own.
func (g *Getter) attempt(ctx context.Context, it Item, cd *cacheDir, name, dest string) (retry bool, err error) {
	stall := g.stallDuration()
	attemptCtx, watch := newStallWatch(ctx, stall)
	defer watch.Stop()
	ref := it.ref()
	classify := func(err error) (bool, error) {
		return classifyAttemptErr(ctx, watch, stall, ref, it.Secret, err)
	}

	return g.stage(ctx, it, cd, name, dest, ref, func(w io.Writer) (bool, error) {
		req, err := newRequest(attemptCtx, it.URL, it.Header, ref, it.Secret)
		if err != nil {
			return false, err
		}
		resp, err := g.clientFor(it).Do(req)
		if err != nil {
			return classify(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			se := &StatusError{URL: it.URL, Status: resp.Status, Code: resp.StatusCode}
			if it.Secret {
				// The reason phrase is the server's to write, and may echo
				// the URL: use the standard text for the code instead.
				se.URL, se.Status = "", fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
				return resp.StatusCode >= 500, fmt.Errorf("%s: %w", it.Name, se)
			}
			return resp.StatusCode >= 500, se
		}

		var src io.Reader = watch.Reader(resp.Body)
		if resp.ContentLength > 64<<20 {
			src = &progress{r: src, total: resp.ContentLength, name: it.Name, log: g.logf, next: time.Now().Add(10 * time.Second)}
		}
		if _, err := io.Copy(w, src); err != nil {
			return classify(err)
		}
		return false, nil
	})
}

// stage is the one place a file takes its name: it makes a temp of its
// own in cd, has fill write the bytes (hashing them as they go), then
// syncs it, makes it 0644 and verifies it -- against the pin, or by
// Check, which sees the temp -- and only then renames it to dest. fill's
// retry says whether its failure is worth another attempt. what names
// where the bytes came from in a mismatch message. The temp is removed on
// every failure.
func (g *Getter) stage(ctx context.Context, it Item, cd *cacheDir, name, dest, what string, fill func(w io.Writer) (retry bool, err error)) (retry bool, err error) {
	f, err := inCacheDir(cd, func() (*os.File, error) { return os.CreateTemp(cd.path, "."+name+".*.part") })
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()

	h := sha256.New()
	if retry, err := fill(io.MultiWriter(f, h)); err != nil {
		return retry, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	// CreateTemp made it 0600; a cache file is 0644 (see Get). This is
	// this attempt's own fresh file, never a link to anyone else's.
	if err := f.Chmod(0o644); err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if it.SHA256 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != it.SHA256 {
			return false, &MismatchError{msg: fmt.Sprintf("checksum mismatch for %s: want %s, got %s; nothing was renamed into place", what, it.SHA256, got)}
		}
	} else if err := it.Check(ctx, tmp); err != nil {
		return false, fmt.Errorf("%s fails its check: %w; nothing was renamed into place", what, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return false, err
	}
	ok = true
	return false, nil
}

// Fetch is the body of the URL, for a small text file (a SHA256SUMS): a
// 200 with at most max bytes, else an error (a *StatusError for any other
// status). It retries and times out stalls as a download does.
func (g *Getter) Fetch(ctx context.Context, url string, max int64) ([]byte, error) {
	var b []byte
	err := g.retryPolicy().do(ctx, url, func() (bool, error) {
		var retry bool
		var err error
		b, retry, err = g.fetchOnce(ctx, url, max)
		return retry, err
	})
	return b, err
}

func (g *Getter) fetchOnce(ctx context.Context, url string, max int64) ([]byte, bool, error) {
	stall := g.stallDuration()
	attemptCtx, watch := newStallWatch(ctx, stall)
	defer watch.Stop()

	req, err := newRequest(attemptCtx, url, nil, url, false)
	if err != nil {
		return nil, false, err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		retry, werr := classifyAttemptErr(ctx, watch, stall, url, false, err)
		return nil, retry, werr
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode >= 500, &StatusError{URL: url, Status: resp.Status, Code: resp.StatusCode}
	}
	// Read one byte past the cap: reaching it means the real response is
	// too big (or unbounded) rather than silently truncating a response
	// that happens to be exactly the cap, or worse, one just over it.
	b, err := io.ReadAll(io.LimitReader(watch.Reader(resp.Body), max+1))
	if err != nil {
		retry, werr := classifyAttemptErr(ctx, watch, stall, url, false, err)
		return nil, retry, werr
	}
	if int64(len(b)) > max {
		return nil, false, fmt.Errorf("%s: response is at least %d bytes -- refusing to buffer more of what should be a small text file", url, len(b))
	}
	return b, false, nil
}

// Transient reports whether err is worth retrying: a network-level
// failure, and never once ctx is done.
func Transient(ctx context.Context, err error) bool {
	return ctx.Err() == nil && classifyErr(err)
}

// stallDuration is StallTimeout, defaulted: 0 or negative means 2
// minutes. A negative value is never meaningful (time.NewTimer treats it
// as "fire immediately", which would abort every attempt as instantly
// stalled), so it gets the same default as unset.
func (g *Getter) stallDuration() time.Duration {
	if g.StallTimeout <= 0 {
		return 2 * time.Minute
	}
	return g.StallTimeout
}

// cleanStaleTemps removes this item's own leftover temp files from an
// earlier attempt that never finished cleanly: a process killed by
// SIGKILL, OOM, or power loss leaves a uniquely named
// ".<name>.<rand>.part" file (attempt's own temp) behind forever, since
// nothing else ever removes it. A live attempt keeps its temp's mtime
// fresh with every byte written, or self-aborts at the stall timeout, so
// anything older than max(10x the stall timeout, 30 minutes) is provably
// dead -- and provably ours, since the name is this package's own
// ".<name>.<rand>.part" pattern, which nothing else is expected to use in
// the caller's directory for this item.
func (g *Getter) cleanStaleTemps(it Item, dir, name string) {
	threshold := 10 * g.stallDuration()
	if threshold < 30*time.Minute {
		threshold = 30 * time.Minute
	}
	cutoff := time.Now().Add(-threshold)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := "." + name + "."
	for _, e := range entries {
		n := e.Name()
		isTempFile := !e.IsDir() && strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".part") && len(n) > len(prefix)+len(".part")
		if !isTempFile {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue // fresh (still being written), or gone already -- leave a live attempt alone
		}
		full := filepath.Join(dir, n)
		if err := os.Remove(full); err == nil {
			g.logf("%s: removed a stale leftover temp %s (older than %v)", it.Name, full, threshold)
		}
	}
}

// classifyErr reports whether a network-level failure is worth retrying.
// TLS/certificate failures and a malformed URL or unsupported scheme are
// permanent -- another attempt cannot fix them -- so only everything else
// (connection failures, timeouts, a short body) is retried.
func classifyErr(err error) bool {
	if err == nil {
		return false
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return false
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return false
	}
	var authErr x509.UnknownAuthorityError
	if errors.As(err, &authErr) {
		return false
	}
	var invalidErr x509.CertificateInvalidError
	if errors.As(err, &invalidErr) {
		return false
	}
	if errors.Is(err, errTooManyRedirects) {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		msg := urlErr.Err.Error()
		if strings.Contains(msg, "unsupported protocol scheme") || strings.Contains(msg, "missing protocol scheme") {
			return false
		}
	}
	return true
}

// stallWatch cancels its context if Reader's returned reader goes d
// without a successful read: a captive portal or a stuck connection can
// hold a socket open, past headers, without ever failing outright.
// Shared by attempt (downloads) and fetchOnce (Fetch), so both
// abort-and-retry a stalled response the same way.
type stallWatch struct {
	cancel  context.CancelFunc
	reset   chan struct{}
	done    chan struct{}
	stalled atomic.Bool
}

func newStallWatch(ctx context.Context, d time.Duration) (context.Context, *stallWatch) {
	attemptCtx, cancel := context.WithCancel(ctx)
	w := &stallWatch{cancel: cancel, reset: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		for {
			select {
			case <-w.reset:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(d)
			case <-timer.C:
				w.stalled.Store(true)
				cancel()
				return
			case <-w.done:
				return
			}
		}
	}()
	return attemptCtx, w
}

// Reader wraps r so every successful read resets the watchdog.
func (w *stallWatch) Reader(r io.Reader) io.Reader { return &stallGuard{r: r, reset: w.reset} }

// Stop releases the watchdog goroutine and the context. Idempotent
// callers defer this exactly once per newStallWatch.
func (w *stallWatch) Stop() {
	close(w.done)
	w.cancel()
}

// classifyAttemptErr turns a network-level failure into (retry, error):
// the overall context being done always wins (no retry); a confirmed
// stall is retryable and says so; everything else goes through
// classifyErr. For a secret item nothing of err itself is kept -- Go's
// *url.Error and a net.OpError carry the URL and the address -- only the
// reason, in words.
func classifyAttemptErr(ctx context.Context, w *stallWatch, stall time.Duration, ref string, secret bool, err error) (bool, error) {
	if ctx.Err() != nil {
		if secret {
			return false, fmt.Errorf("%s: %w", ref, ctx.Err())
		}
		return false, err
	}
	if w.stalled.Load() {
		if secret {
			return true, fmt.Errorf("%s: no data for %v", ref, stall)
		}
		return true, fmt.Errorf("%s: no data for %v: %w", ref, stall, err)
	}
	if secret {
		return classifyErr(err), fmt.Errorf("%s: %s", ref, reason(err))
	}
	return classifyErr(err), fmt.Errorf("%s: %w", ref, err)
}

// reason says in words why err happened, from a short list that cannot
// carry an address, a host or a URL.
func reason(err error) string {
	var errno syscall.Errno
	var dns *net.DNSError
	var ne net.Error
	var hostErr x509.HostnameError
	var authErr x509.UnknownAuthorityError
	var invalidErr x509.CertificateInvalidError
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.Is(err, errTooManyRedirects):
		return errTooManyRedirects.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "the connection ended early"
	case errors.Is(err, io.EOF):
		return "the connection closed"
	case errors.As(err, &errno):
		return errno.Error()
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "no such host"
		}
		return "the host name could not be resolved"
	case errors.As(err, &hostErr), errors.As(err, &authErr), errors.As(err, &invalidErr), errors.As(err, &certErr):
		return "the certificate was not accepted"
	case errors.As(err, &ne) && ne.Timeout():
		return "timed out"
	}
	return "the request failed"
}

// stallGuard signals reset on every byte read, so a stallWatch's
// goroutine can tell "still receiving data" from "the connection is stuck".
type stallGuard struct {
	r     io.Reader
	reset chan<- struct{}
}

func (s *stallGuard) Read(b []byte) (int, error) {
	n, err := s.r.Read(b)
	if n > 0 {
		select {
		case s.reset <- struct{}{}:
		default:
		}
	}
	return n, err
}

type progress struct {
	r           io.Reader
	done, total int64
	name        string
	log         func(string, ...any)
	next        time.Time
}

func (p *progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if time.Now().After(p.next) {
		p.log("%s: %d of %d MiB", p.name, p.done>>20, p.total>>20)
		p.next = time.Now().Add(10 * time.Second)
	}
	return n, err
}

// ctxCheckEvery is how many bytes ctxReader reads between looks at its
// context: a few MiB is a few milliseconds of hashing or copying.
const ctxCheckEvery = 4 << 20

// ctxReader is r, failing with ctx's error once ctx is done. It looks at
// ctx before its first read and then every ctxCheckEvery bytes. A read
// that blocks forever still blocks: cancelling ctx unblocks it only
// between checks, not mid-syscall.
type ctxReader struct {
	ctx   context.Context
	r     io.Reader
	since int
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if c.since == 0 {
		if err := c.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := c.r.Read(b)
	c.since += n
	if c.since >= ctxCheckEvery {
		c.since = 0
	}
	return n, err
}

func (g *Getter) logf(format string, a ...any) {
	if g.Log != nil {
		g.Log(format, a...)
	}
}

// SHA256File is path's SHA-256, as lowercase hex, giving up with ctx's
// error once ctx is done: hashing InstallESD.dmg reads 5.2 GB, and a
// Ctrl-C should not have to wait for it.
func SHA256File(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
