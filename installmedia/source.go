package installmedia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mavergreen/longboat/installmedia/download"
)

// Kind is how a source gets media. It is always stated: nothing here
// guesses a kind from what a path or a URL looks like.
type Kind string

const (
	KindRecipe  Kind = "recipe"  // the release's built-in Recipe
	KindFile    Kind = "file"    // a local Path, or a URL with an optional AuthHeader
	KindCommand Kind = "command" // a program on this host that writes the media into a directory it is given
)

// Source is one place media may come from. URL and AuthHeader are
// secrets: no error, log line or file name here ever carries them.
//
// No kind fetches code from a URL and runs it: a command is a program
// already on this host, and a file, however it arrives, is only read.
type Source struct {
	Kind       Kind
	Path       string   // KindFile: a local file
	URL        string   // KindFile: an http or https URL
	AuthHeader string   // KindFile with URL: "Name: value", sent with the request
	Command    []string // KindCommand: an absolute program and its arguments
}

// withheld is how a URL source is named everywhere: its URL is a secret.
const withheld = "file at a URL (withheld)"

// String names s for messages, never by its URL.
func (s Source) String() string {
	switch s.Kind {
	case KindRecipe:
		return "recipe"
	case KindFile:
		if s.URL != "" {
			return withheld
		}
		if strings.Contains(s.Path, "://") {
			// A URL given as the path, which Validate refuses: it may be
			// a secret one, so it is never printed either.
			return "file at a URL-like path (withheld)"
		}
		return "file " + s.Path
	case KindCommand:
		if len(s.Command) > 0 && strings.Contains(s.Command[0], "://") {
			// A URL given as the program, which Validate refuses: it may
			// be a secret one, so it is never printed either.
			return "command at a URL-like path (withheld)"
		}
		if len(s.Command) > 0 {
			return "command " + filepath.Base(s.Command[0])
		}
		return "command"
	}
	return fmt.Sprintf("kind %q", string(s.Kind))
}

// Validate refuses a source that is not exactly one of the three kinds,
// fully stated. Its errors never carry the URL or the auth header.
func (s Source) Validate() error {
	switch s.Kind {
	case "":
		return errors.New("kind is required: recipe, file or command")
	case KindRecipe:
		if s.Path != "" || s.URL != "" || s.AuthHeader != "" || len(s.Command) > 0 {
			return errors.New("a recipe source takes no path, URL, auth header or command")
		}
	case KindFile:
		if len(s.Command) > 0 {
			return errors.New("a file source takes no command: a command is a source of its own kind")
		}
		if s.Path != "" && s.URL != "" {
			return errors.New("a file source takes a path or a URL, not both")
		}
		if s.Path == "" && s.URL == "" {
			return errors.New("a file source needs a path or a URL")
		}
		if strings.Contains(s.Path, "://") {
			return errors.New("a file source's path looks like a URL: give it as the url, which is kept secret")
		}
		if s.URL != "" {
			if err := validateURL(s.URL); err != nil {
				return err
			}
		}
		if s.AuthHeader != "" {
			if s.URL == "" {
				return errors.New("an auth header goes with a file source's URL, and this one has none")
			}
			if _, err := parseAuthHeader(s.AuthHeader); err != nil {
				return err
			}
		}
	case KindCommand:
		if s.Path != "" || s.URL != "" || s.AuthHeader != "" {
			return errors.New("a command source takes no path, URL or auth header: its command writes the file")
		}
		if len(s.Command) == 0 || s.Command[0] == "" {
			return errors.New("a command source needs a command to run")
		}
		if strings.Contains(s.Command[0], "://") {
			return errors.New("a command source runs a program already on this host, never one named by a URL")
		}
		if !filepath.IsAbs(s.Command[0]) {
			return errors.New("a command source's program must be an absolute path")
		}
	default:
		return fmt.Errorf("kind %q is not one of recipe, file or command", string(s.Kind))
	}
	return nil
}

// validateURL refuses a URL that is not http or https. url.Parse's own
// error quotes the URL, so it is never passed on.
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("a file source's URL does not parse as a URL")
	}
	if s := strings.ToLower(u.Scheme); (s != "http" && s != "https") || u.Host == "" {
		return errors.New("a file source's URL must be an http or https URL with a host")
	}
	return nil
}

// parseAuthHeader is "Name: value" as a header to send. Its errors never
// quote the header.
func parseAuthHeader(h string) (http.Header, error) {
	bad := errors.New(`an auth header must be "Name: value": a header name, a colon and a value, on one line`)
	name, value, ok := strings.Cut(h, ":")
	value = strings.Trim(value, " \t")
	if !ok || name == "" || value == "" {
		return nil, bad
	}
	for _, c := range name {
		if c >= 0x7f || c <= ' ' || strings.ContainsRune(`"(),/:;<=>?@[\]{}`, c) {
			return nil, bad
		}
	}
	for _, c := range value {
		if (c < ' ' && c != '\t') || c == 0x7f {
			return nil, bad
		}
	}
	hdr := http.Header{}
	hdr.Set(name, value)
	return hdr, nil
}

// DefaultSources is what a release uses when no source is named: its
// built-in recipe, if it has one.
func DefaultSources(rel Release) ([]Source, error) {
	if rel.Recipe == nil {
		return nil, noRecipe(rel)
	}
	return []Source{{Kind: KindRecipe}}, nil
}

func noRecipe(rel Release) error {
	return fmt.Errorf("version %s has no built-in recipe: name a file or command source", rel.Name)
}

// Options are what Obtain and Resolve need from their caller.
type Options struct {
	Getter  *download.Getter     // the cache and the network: for a recipe, a URL, and every source of a pinned release
	WorkDir string               // where a command's scratch directories go
	Log     func(string, ...any) // nil logs nowhere
	Output  io.Writer            // a command's stdout and stderr; nil discards them
}

func (o Options) logf(format string, a ...any) {
	if o.Log != nil {
		o.Log(format, a...)
	}
}

// Candidate is media a source gave. Verified is set when it has already
// been held to the release's pins; otherwise it still needs Check.
type Candidate struct {
	Path     string
	Verified *Delivered

	scratch string // a command's scratch directory, which holds Path
}

// Discard removes the candidate's scratch directory, if it has one. A
// user's own file, and a file in the cache, are never removed.
func (c Candidate) Discard() error {
	if c.scratch == "" {
		return nil
	}
	return removeScratch(c.scratch)
}

// Obtain gets media from one source. For a release pinned by a
// whole-file SHA-256, every kind ends in the cache, verified. For an
// unpinned release, a path is used where it is, a URL is downloaded into
// the cache and checked there, and a command's file stays in its scratch
// directory under o.WorkDir, unverified, for the caller to Check (and
// Discard if it fails).
//
// Its errors do not name the source: a caller names it by String.
func Obtain(ctx context.Context, rel Release, s Source, o Options) (Candidate, error) {
	if err := s.Validate(); err != nil {
		return Candidate{}, err
	}
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	switch s.Kind {
	case KindRecipe:
		return obtainRecipe(ctx, rel, o)
	case KindFile:
		if s.URL != "" {
			return obtainURL(ctx, rel, s, o)
		}
		return obtainPath(ctx, rel, s.Path, o)
	default:
		return obtainCommand(ctx, rel, s, o)
	}
}

func needGetter(o Options) error {
	if o.Getter == nil {
		return errors.New("installmedia: Options.Getter is required for this source")
	}
	return nil
}

// pin is the item a pinned release's file is held to. Its Filename is
// always the release's FileName, never the name of wherever the bytes
// came from.
func pin(rel Release) download.Item {
	return download.Item{Name: rel.Whole.Name, URL: rel.Whole.URL, SHA256: rel.Whole.SHA256, Filename: rel.FileName}
}

// pinned is a candidate the pin has already verified.
func pinned(rel Release, path string) Candidate {
	return Candidate{Path: path, Verified: &Delivered{Release: rel.Name, Build: rel.Whole.Build, ContentDigest: rel.Whole.SHA256, Path: path}}
}

func obtainRecipe(ctx context.Context, rel Release, o Options) (Candidate, error) {
	if rel.Recipe == nil {
		return Candidate{}, noRecipe(rel)
	}
	if rel.Whole == nil {
		return Candidate{}, fmt.Errorf("version %s's recipe has no whole-file pin to hold its file to", rel.Name)
	}
	if err := needGetter(o); err != nil {
		return Candidate{}, err
	}
	p, err := rel.Recipe(ctx, o.Getter, pin(rel))
	if err != nil {
		return Candidate{}, err
	}
	return pinned(rel, p), nil
}

func obtainPath(ctx context.Context, rel Release, path string, o Options) (Candidate, error) {
	if rel.Whole != nil {
		if err := needGetter(o); err != nil {
			return Candidate{}, err
		}
		p, err := o.Getter.Import(ctx, pin(rel), path)
		if err != nil {
			return Candidate{}, err
		}
		return pinned(rel, p), nil
	}
	// Stat, not open: opening a FIFO would block, past any cancel.
	fi, err := os.Stat(path)
	if err != nil {
		return Candidate{}, err
	}
	if !fi.Mode().IsRegular() {
		return Candidate{}, fmt.Errorf("%s is not a regular file", path)
	}
	return Candidate{Path: path}, nil
}

func obtainURL(ctx context.Context, rel Release, s Source, o Options) (Candidate, error) {
	if err := needGetter(o); err != nil {
		return Candidate{}, err
	}
	var header http.Header
	if s.AuthHeader != "" {
		h, err := parseAuthHeader(s.AuthHeader)
		if err != nil {
			return Candidate{}, err
		}
		header = h
	}
	// A Secret item: the download layer names it by Name, never by its
	// URL or header, and the release's FileName names the file.
	it := download.Item{Name: rel.FileName, URL: s.URL, Header: header, Filename: rel.FileName, Secret: true}
	if rel.Whole != nil {
		it.SHA256 = rel.Whole.SHA256
		p, err := o.Getter.Get(ctx, it)
		if err != nil {
			return Candidate{}, err
		}
		return pinned(rel, p), nil
	}
	// Unpinned media is cached under the SHA-256 of its URL, so the
	// cache's directory name -- and the cache path that Check's errors
	// name, which can reach a build's log -- carries a hash of a secret.
	// A hash confirms a guess but recovers nothing: the URL's secret part
	// (a token, a presigned signature) is expected to be unguessable, and
	// the auth header is not hashed at all.
	key := sha256.Sum256([]byte(s.URL))
	it.Key = hex.EncodeToString(key[:])
	// The download layer reports Check's error as is, so it must not
	// carry the URL. Check's errors name only the path, which is in the
	// cache, named by the key and FileName.
	var verified *Delivered
	it.Check = func(ctx context.Context, path string) error {
		d, err := Check(ctx, rel, path)
		if err != nil {
			return err
		}
		verified = &d
		return nil
	}
	p, err := o.Getter.Get(ctx, it)
	if err != nil {
		return Candidate{}, err
	}
	if verified == nil {
		return Candidate{}, fmt.Errorf("%s came back from the cache unchecked", rel.FileName)
	}
	verified.Path = p
	return Candidate{Path: p, Verified: verified}, nil
}

// obtainCommand runs the command in a scratch directory of its own
// under o.WorkDir, with a stripped environment, and takes the one
// regular file it leaves in its output directory: the last argument it
// is given. For a pinned release that file is imported into the cache
// and the scratch removed.
func obtainCommand(ctx context.Context, rel Release, s Source, o Options) (c Candidate, err error) {
	if o.WorkDir == "" {
		return Candidate{}, errors.New("installmedia: Options.WorkDir is required for a command source")
	}
	if rel.Whole != nil {
		if err := needGetter(o); err != nil {
			return Candidate{}, err
		}
	}
	if err := os.MkdirAll(o.WorkDir, 0o755); err != nil {
		return Candidate{}, err
	}
	scratch, err := os.MkdirTemp(o.WorkDir, "command-")
	if err != nil {
		return Candidate{}, err
	}
	// The scratch goes, unless the candidate is in it. Failing to remove
	// it is reported, but does not undo media already in hand.
	defer func() {
		if c.scratch == "" {
			if rerr := removeScratch(scratch); rerr != nil {
				o.logf("removing a command's scratch directory %s: %v", scratch, rerr)
			}
		}
	}()
	out := filepath.Join(scratch, "out")
	home := filepath.Join(scratch, "home")
	tmp := filepath.Join(scratch, "tmp")
	for _, d := range []string{out, home, tmp} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return Candidate{}, err
		}
	}
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + tmp, "LANG=C", "LONGBOAT_RELEASE=" + rel.Name}
	argv := append(append([]string(nil), s.Command...), out)
	if err := runCommand(ctx, argv, scratch, env, o); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return Candidate{}, fmt.Errorf("the command was stopped: %w", cerr)
		}
		return Candidate{}, err
	}
	file, err := theOneFile(out)
	if err != nil {
		return Candidate{}, fmt.Errorf("the command %w", err)
	}
	if rel.Whole != nil {
		p, err := o.Getter.Import(ctx, pin(rel), file)
		if err != nil {
			return Candidate{}, err
		}
		return pinned(rel, p), nil
	}
	// The candidate needs only out/: whatever the command staged in its
	// home or temp directory, perhaps a second copy of a disc, goes now.
	for _, d := range []string{home, tmp} {
		if err := removeScratch(d); err != nil {
			o.logf("removing a command's scratch directory %s: %v", d, err)
		}
	}
	return Candidate{Path: file, scratch: scratch}, nil
}

// theOneFile is the one regular file in dir, made 0644. Anything else
// in dir -- nothing, a second file, a symbolic link, a directory, a file
// with another hard link -- is refused.
func theOneFile(dir string) (string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	switch len(ents) {
	case 0:
		return "", errors.New("wrote no file")
	case 1:
	default:
		return "", fmt.Errorf("wrote %d files, not one", len(ents))
	}
	e := ents[0]
	switch {
	case e.Type()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("wrote %s as a symbolic link, not a file", e.Name())
	case !e.Type().IsRegular():
		return "", fmt.Errorf("wrote %s, which is not a regular file", e.Name())
	}
	p := filepath.Join(dir, e.Name())
	if err := makeReadable(p); err != nil {
		return "", fmt.Errorf("wrote %s, which %w", e.Name(), err)
	}
	return p, nil
}

// removeScratch removes a command's scratch directory, even where the
// command left a directory nobody may write: each directory is made
// writable first. WalkDir never follows a symbolic link, so only the
// scratch's own directories are changed.
func removeScratch(dir string) error {
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// Try is one source Resolve tried, named by its String, and why it
// failed.
type Try struct {
	Source string
	Err    error
}

// ResolveError is every source Resolve tried for a release, in order,
// and why each failed.
type ResolveError struct {
	Release string
	Tries   []Try
}

func (e *ResolveError) Error() string {
	parts := make([]string, len(e.Tries))
	for i, t := range e.Tries {
		parts[i] = t.Source + ": " + t.Err.Error()
	}
	return fmt.Sprintf("no source gave version %s's media: %s", e.Release, strings.Join(parts, "; "))
}

// Unwrap is every try's error, so errors.Is sees a cancel.
func (e *ResolveError) Unwrap() []error {
	errs := make([]error, len(e.Tries))
	for i, t := range e.Tries {
		errs[i] = t.Err
	}
	return errs
}

// Resolve tries each source in order and returns the first media that
// passes rel's check. Every source is validated before any is tried. A
// candidate already verified is not checked again; one that fails its
// check is discarded. A cancel stops it after the source it cancelled.
func Resolve(ctx context.Context, rel Release, sources []Source, o Options) (Delivered, error) {
	if len(sources) == 0 {
		return Delivered{}, fmt.Errorf("version %s: no media sources to try", rel.Name)
	}
	for i, s := range sources {
		if err := s.Validate(); err != nil {
			return Delivered{}, fmt.Errorf("version %s: source %d (%s): %w", rel.Name, i+1, s, err)
		}
	}
	re := &ResolveError{Release: rel.Name}
	for _, s := range sources {
		d, err := resolveOne(ctx, rel, s, o)
		if err == nil {
			o.logf("version %s: media from %s", rel.Name, s)
			return d, nil
		}
		o.logf("version %s: %s failed: %v", rel.Name, s, err)
		re.Tries = append(re.Tries, Try{Source: s.String(), Err: err})
		if ctx.Err() != nil {
			break
		}
	}
	return Delivered{}, re
}

func resolveOne(ctx context.Context, rel Release, s Source, o Options) (Delivered, error) {
	c, err := Obtain(ctx, rel, s, o)
	if err != nil {
		return Delivered{}, err
	}
	if c.Verified != nil {
		return *c.Verified, nil
	}
	d, err := Check(ctx, rel, c.Path)
	if err != nil {
		if derr := c.Discard(); derr != nil {
			o.logf("version %s: removing %s's scratch: %v", rel.Name, s, derr)
		}
		return Delivered{}, err
	}
	return d, nil
}

// copyOutput copies a command's output from r to w. Whatever stops the
// copy short -- a writer that fails, or one that writes short -- must not
// stop the reading: the command would fill its pipe and block in write(2)
// for good. So the rest is always drained to nowhere, which ends at once
// after a read that failed. Only w's own failure is logged: a read that
// fails is the pipe closed under it once the wait for leftovers ran out,
// the end of the output.
func copyOutput(w io.Writer, r io.Reader, logf func(string, ...any)) {
	rw := &recordingWriter{w: w}
	io.Copy(rw, r)
	io.Copy(io.Discard, r)
	if rw.err != nil {
		logf("the command's output could not be written: %v", rw.err)
	}
}

// recordingWriter is w, remembering the first error it returned.
type recordingWriter struct {
	w   io.Writer
	err error
}

func (r *recordingWriter) Write(b []byte) (int, error) {
	n, err := r.w.Write(b)
	if err != nil && r.err == nil {
		r.err = err
	}
	return n, err
}
