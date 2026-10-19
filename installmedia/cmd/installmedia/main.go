// Command installmedia lists, fetches and verifies Mac OS X install media.
// It is what `longboat media list`, `longboat media fetch` and
// `longboat media verify` run, and it speaks as them.
//
//	installmedia list
//	installmedia fetch <version> --into DIR [source flags]
//	installmedia verify <version> <image>
//	installmedia --version
//
// Exit codes: 0 ok, 1 refused or failed, 2 usage.
//
// A URL and an auth header are secrets: neither is ever printed. The
// header is only ever read from the environment variable that
// --auth-header-env names, never from the command line.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/Mavergreen/longboat/installmedia"
	"github.com/Mavergreen/longboat/installmedia/download"
)

// version is set at build time with -X main.version=<version>.
var version = "dev"

// lookup finds a release by name; a test points it at a fixture.
var lookup = installmedia.Lookup

const synopsis = `usage: longboat media list
       longboat media fetch <version> --into DIR [--source-recipe] [--source-file PATH] [--source-url URL [--auth-header-env NAME]] [--source-command PATH]
       longboat media verify <version> <image>
`

const details = `
list says, for each version, whether Longboat can fetch its media itself (recipe) or needs yours.
fetch puts <version>'s install media in DIR, held to its pins, and prints its path.
  Sources are tried in the order given, and each flag may be given more than once:
    --source-recipe          the version's built-in recipe (10.9: Apple's servers)
    --source-file PATH       a disc image on this host
    --source-url URL         a disc image to download; the URL is never printed
    --auth-header-env NAME   the download's "Name: value" header, read from the environment
                             variable NAME; it follows its --source-url and is never printed
    --source-command PATH    a program on this host, run with a directory to write the image into
  With no source, it uses the version's built-in recipe. 10.6 has none: bring your own disc.
verify holds <image> to <version>'s pins and prints <version> <build> <content-digest>.
Exit status: 0 ok, 1 refused or failed, 2 usage.
`

// usageError is a mistake in the arguments: exit 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

// run is the command, given its arguments (without the program's name),
// its output streams and its environment.
func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Log lines and a command's output can arrive from more than one
	// goroutine.
	stderr = &lockedWriter{w: stderr}

	var err error
	switch {
	case len(args) == 1 && args[0] == "--version":
		fmt.Fprintf(stdout, "installmedia %s\n", version)
		return 0
	case len(args) == 1 && (args[0] == "--help" || args[0] == "-h"):
		io.WriteString(stdout, synopsis+details)
		return 0
	case len(args) > 0 && args[0] == "list":
		err = list(args[1:], stdout)
	case len(args) > 0 && args[0] == "fetch":
		err = fetch(ctx, args[1:], stdout, stderr, getenv)
	case len(args) > 0 && args[0] == "verify":
		err = verify(ctx, args[1:], stdout)
	default:
		err = usagef("")
	}
	var ue *usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		if ue.msg != "" {
			fmt.Fprintf(stderr, "longboat media: %s\n", ue.msg)
		}
		io.WriteString(stderr, synopsis)
		return 2
	default:
		fmt.Fprintf(stderr, "longboat media: %v\n", err)
		return 1
	}
}

// release is the release named by args, which start "<version>".
func release(cmd string, args []string) (installmedia.Release, error) {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return installmedia.Release{}, usagef("%s takes <version>", cmd)
	}
	if !releaseName.MatchString(args[0]) {
		// It may be a secret in the wrong place: never echoed.
		return installmedia.Release{}, usagef("%s takes a version, such as 10.9, and was given something else (withheld)", cmd)
	}
	rel, err := lookup(args[0])
	if err != nil {
		return installmedia.Release{}, usagef("no install media for version %q; offered: %s", args[0], strings.Join(versions(), " "))
	}
	return rel, nil
}

// versions are the releases this module knows, oldest first.
func versions() []string {
	rels := installmedia.Releases()
	names := make([]string, len(rels))
	for i, r := range rels {
		names[len(rels)-1-i] = r.Name
	}
	return names
}

// list prints each release, oldest first, with whether it has a built-in recipe.
func list(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return usagef("list takes no arguments")
	}
	rels := installmedia.Releases()
	for i := len(rels) - 1; i >= 0; i-- {
		how := "bring-your-own"
		if rels[i].Recipe != nil {
			how = "recipe"
		}
		fmt.Fprintf(stdout, "%s %s\n", rels[i].Name, how)
	}
	return nil
}

// looksLikeURL is an argument that may be a secret URL, so it is never
// echoed.
func looksLikeURL(s string) bool { return strings.Contains(s, "://") }

func verify(ctx context.Context, args []string, stdout io.Writer) error {
	rel, err := release("verify", args)
	if err != nil {
		return err
	}
	if len(args) != 2 {
		return usagef("verify takes <version> <image>")
	}
	image := args[1]
	if looksLikeURL(image) {
		return usagef("verify reads a file on this host, and the image given looks like a URL (withheld)")
	}
	d, err := installmedia.Check(ctx, rel, image)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s %s %s\n", d.Release, d.Build, d.ContentDigest)
	return nil
}

// releaseName is how a release may be spelled, safe to echo back.
var releaseName = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.]*$`)

// plainFlag is a flag's spelling alone, safe to echo back.
var plainFlag = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]*$`)

// envName is a portable environment variable name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// fetchArgs are fetch's arguments, parsed.
type fetchArgs struct {
	rel     installmedia.Release
	into    string
	sources []installmedia.Source // in the order given, or the release's default
}

func parseFetch(args []string, getenv func(string) string) (fetchArgs, error) {
	var fa fetchArgs
	rel, err := release("fetch", args)
	if err != nil {
		return fa, err
	}
	fa.rel = rel
	for i := 1; i < len(args); i++ {
		flag := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return "", usagef("%s needs a value", flag)
			}
			i++
			return args[i], nil
		}
		switch flag {
		case "--into":
			if fa.into != "" {
				return fa, usagef("--into is given twice")
			}
			if fa.into, err = value(); err != nil {
				return fa, err
			}
			if looksLikeURL(fa.into) {
				return fa, usagef("--into takes a directory on this host, and the one given looks like a URL (withheld)")
			}
			// Clean once, so every path made from it agrees with mkdirs.
			fa.into = filepath.Clean(fa.into)
		case "--source-recipe":
			fa.sources = append(fa.sources, installmedia.Source{Kind: installmedia.KindRecipe})
		case "--source-file":
			v, err := value()
			if err != nil {
				return fa, err
			}
			fa.sources = append(fa.sources, installmedia.Source{Kind: installmedia.KindFile, Path: v})
		case "--source-url":
			v, err := value()
			if err != nil {
				return fa, err
			}
			fa.sources = append(fa.sources, installmedia.Source{Kind: installmedia.KindFile, URL: v})
		case "--source-command":
			v, err := value()
			if err != nil {
				return fa, err
			}
			fa.sources = append(fa.sources, installmedia.Source{Kind: installmedia.KindCommand, Command: []string{v}})
		case "--auth-header-env":
			n := len(fa.sources)
			if n == 0 || fa.sources[n-1].URL == "" || fa.sources[n-1].AuthHeader != "" {
				return fa, usagef("--auth-header-env follows the --source-url whose header it names, once")
			}
			name, err := value()
			if err != nil {
				return fa, err
			}
			if !envName.MatchString(name) {
				// It may be the header itself, pasted in: never echoed.
				return fa, usagef("--auth-header-env takes the name of an environment variable that holds the header, not the header")
			}
			h := getenv(name)
			if h == "" {
				return fa, usagef("--auth-header-env names %s, which is unset or empty", name)
			}
			fa.sources[n-1].AuthHeader = h
		default:
			if !plainFlag.MatchString(flag) {
				// It may be a secret that lost its flag: never echoed.
				return fa, usagef("fetch does not take an argument it was given (withheld: it may be a URL or a header); a URL goes after --source-url")
			}
			return fa, usagef("fetch does not take %q", flag)
		}
	}
	if fa.into == "" {
		return fa, usagef("fetch needs --into DIR")
	}
	if len(fa.sources) == 0 {
		def, err := installmedia.DefaultSources(fa.rel)
		if err != nil {
			return fa, &usageError{err.Error() + " (--source-file, --source-url or --source-command)"}
		}
		fa.sources = def
	}
	for i, s := range fa.sources {
		if err := s.Validate(); err != nil {
			return fa, usagef("source %d (%s): %v", i+1, s, err)
		}
	}
	return fa, nil
}

func fetch(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) (err error) {
	fa, err := parseFetch(args, getenv)
	if err != nil {
		return err
	}
	rel := fa.rel
	logf := func(format string, a ...any) { fmt.Fprintf(stderr, "longboat media: "+format+"\n", a...) }
	made, err := mkdirs(fa.into)
	// A fetch that fails takes back the directories it made, if they are
	// still empty; a DIR that was already there stays.
	defer func() {
		if err != nil {
			for i := len(made) - 1; i >= 0; i-- {
				if os.Remove(made[i]) != nil {
					break
				}
			}
		}
	}()
	if err != nil {
		return err
	}
	dest := filepath.Join(fa.into, rel.FileName)
	o := installmedia.Options{
		// Media goes straight into DIR, under the release's file name. A
		// file already there is held to the pins, and kept if it passes.
		Getter: &download.Getter{Path: func(_, name string) string { return filepath.Join(fa.into, name) }, Log: logf},
		Log:    logf,
		Output: stderr,
	}
	if hasCommand(fa.sources) {
		// A command writes into scratch in DIR, so its file reaches DIR
		// by a rename, not a copy.
		if o.WorkDir, err = os.MkdirTemp(fa.into, ".installmedia-work-"); err != nil {
			return err
		}
		defer func() {
			if rerr := os.RemoveAll(o.WorkDir); rerr != nil {
				logf("removing %s: %v", o.WorkDir, rerr)
			}
		}()
	}
	d, err := installmedia.Resolve(ctx, rel, fa.sources, o)
	if err != nil {
		return err
	}
	if err := place(ctx, rel, d.Path, dest, o.WorkDir); err != nil {
		return err
	}
	fmt.Fprintln(stdout, dest)
	return nil
}

// mkdirs is os.MkdirAll(dir), returning the directories it made,
// outermost first.
func mkdirs(dir string) ([]string, error) {
	var missing []string
	for d := filepath.Clean(dir); ; {
		if _, err := os.Lstat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append([]string{d}, missing...)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	var made []string
	for _, d := range missing {
		if err := os.Mkdir(d, 0o755); errors.Is(err, fs.ErrExist) {
			continue // made by someone else meanwhile: not this run's
		} else if err != nil {
			return made, err
		}
		made = append(made, d)
	}
	return made, nil
}

func hasCommand(sources []installmedia.Source) bool {
	for _, s := range sources {
		if s.Kind == installmedia.KindCommand {
			return true
		}
	}
	return false
}

// place puts delivered media at dest. Media the Getter placed is already
// there. A command's file, in scratch in the same directory, is renamed
// there. A user's own file is copied, and the copy checked again before
// it takes its name, so what lands in DIR is exactly what was checked.
func place(ctx context.Context, rel installmedia.Release, path, dest, work string) error {
	if path == dest {
		return nil
	}
	if pi, err := os.Stat(path); err == nil {
		if di, err := os.Stat(dest); err == nil && os.SameFile(pi, di) {
			return nil
		}
	}
	if work != "" && strings.HasPrefix(path, work+string(filepath.Separator)) {
		return os.Rename(path, dest)
	}
	return copyChecked(ctx, rel, path, dest)
}

func copyChecked(ctx context.Context, rel installmedia.Release, src, dest string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*.partial")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = io.Copy(tmp, &ctxReader{ctx: ctx, r: in}); err != nil {
		return fmt.Errorf("copying %s into %s: %w", src, filepath.Dir(dest), err)
	}
	if err = tmp.Chmod(0o644); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if _, err = installmedia.Check(ctx, rel, tmp.Name()); err != nil {
		return fmt.Errorf("the copy of %s: %w", src, err)
	}
	return os.Rename(tmp.Name(), dest)
}

// ctxReader stops reading once ctx is done, so a signal stops a copy.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

// lockedWriter serializes writes to w.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}
