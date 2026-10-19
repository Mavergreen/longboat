// Package installmedia knows the Mac OS X releases whose install media
// the family builds from, and checks a file against their pins. It
// imports nothing outside the standard library.
package installmedia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/Mavergreen/longboat/installmedia/download"
	"github.com/Mavergreen/longboat/installmedia/osrecovery"
)

// WholeFile pins an image by the SHA-256 of the whole file: one that
// only ever exists as Apple published it, byte for byte.
type WholeFile struct{ Name, URL, SHA256, Build string }

// Disc pins one retail disc by the SHA-256 of every file in its Packages
// directory, so any faithful copy passes whatever tool made the image.
type Disc struct {
	Build string
	Sums  map[string]string // package file name -> sha256
}

// Recipe gets a release's media when it is not simply a URL: it returns
// the verified file's path, from the cache or by whatever means the
// release needs. want carries the pin and the file name.
type Recipe func(ctx context.Context, g *download.Getter, want download.Item) (string, error)

// Release is one Mac OS X release and the pins its install media is
// held to: Whole for an image fetched as a file, Discs for a disc the
// user images themselves.
type Release struct {
	Name     string // "10.9" | "10.6"
	Codename string // "mavericks" | "snowleopard"
	FileName string // "InstallESD.dmg" | "install-disc.img"
	Whole    *WholeFile
	Discs    []Disc
	Recipe   Recipe // how Whole is fetched; nil when the release has none
}

// Delivered is media that passed Check: which release and build it is,
// its content digest, and where it is.
type Delivered struct{ Release, Build, ContentDigest, Path string }

// Releases returns every release this module knows, newest first. Each
// call returns fresh copies, so a caller may change what it gets.
func Releases() []Release {
	rels, err := releases()
	if err != nil {
		panic("installmedia: the embedded pins do not parse: " + err.Error())
	}
	return rels
}

// Lookup returns the release named name ("10.9" or "10.6").
func Lookup(name string) (Release, error) {
	rels, err := releases()
	if err != nil {
		return Release{}, err
	}
	var names []string
	for _, r := range rels {
		if r.Name == name {
			return r, nil
		}
		names = append(names, r.Name)
	}
	return Release{}, fmt.Errorf("no version %q: this module knows %s", name, strings.Join(names, ", "))
}

func releases() ([]Release, error) {
	esd, err := pins.ReadFile("pins/installesd-10.9.tsv")
	if err != nil {
		return nil, err
	}
	whole, err := parseWholeFile(esd)
	if err != nil {
		return nil, err
	}
	sl, err := pins.ReadFile("pins/snowleopard-packages.sha256")
	if err != nil {
		return nil, err
	}
	discs, err := parseDiscPins(sl)
	if err != nil {
		return nil, err
	}
	return []Release{
		{Name: "10.9", Codename: "mavericks", FileName: "InstallESD.dmg", Whole: &whole, Recipe: osrecovery.Recovery{}.Fetch},
		{Name: "10.6", Codename: "snowleopard", FileName: "install-disc.img", Discs: discs},
	}, nil
}

// DiscDigest is the SHA-256 of d's pinned lines, "<sha256>  <name>\n",
// sorted bytewise: the content digest of every faithful copy of d.
func DiscDigest(d Disc) string {
	lines := make([]string, 0, len(d.Sums))
	for name, sum := range d.Sums {
		lines = append(lines, sum+"  "+name+"\n")
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return hex.EncodeToString(h.Sum(nil))
}
