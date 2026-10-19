package installmedia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/Mavergreen/longboat/installmedia/hfs"
	"github.com/Mavergreen/longboat/installmedia/image"
)

// CheckError is media that is not what its release pins: which release
// and build it claimed to be, and every way it differs.
type CheckError struct {
	Release, Build string
	Problems       []string
}

func (e *CheckError) Error() string {
	return fmt.Sprintf("the disc is not build %s as it shipped: %s", e.Build, strings.Join(e.Problems, "; "))
}

// chunk is how much Check reads between looks at its context, so a
// cancel stops a 5 GB hash within one chunk.
const chunk = 4 << 20

// Check holds the file at path to rel's pins and says what it is.
func Check(ctx context.Context, rel Release, path string) (Delivered, error) {
	return check(ctx, rel, path, nil)
}

// check is Check, calling onChunk before each read, so a test can cancel
// partway through.
func check(ctx context.Context, rel Release, path string, onChunk func()) (Delivered, error) {
	if rel.Whole == nil {
		if len(rel.Discs) > 0 {
			return checkDisc(ctx, rel, path, onChunk)
		}
		return Delivered{}, fmt.Errorf("version %s: no content check yet", rel.Name)
	}
	f, err := os.Open(path)
	if err != nil {
		return Delivered{}, err
	}
	defer f.Close()
	sum, err := hashReader(ctx, f, onChunk)
	if err != nil {
		return Delivered{}, fmt.Errorf("checking %s: %w", path, err)
	}
	if sum != rel.Whole.SHA256 {
		return Delivered{}, fmt.Errorf("%s has sha256 %s, not the pinned %s", path, sum, rel.Whole.SHA256)
	}
	return Delivered{Release: rel.Name, Build: rel.Whole.Build, ContentDigest: sum, Path: path}, nil
}

// hashReader is the hex SHA-256 of what r yields, looking at ctx before
// each chunk-sized read.
func hashReader(ctx context.Context, r io.Reader, onChunk func()) (string, error) {
	h := sha256.New()
	buf := make([]byte, chunk)
	for {
		if onChunk != nil {
			onChunk()
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := io.ReadFull(r, buf)
		h.Write(buf[:n])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

const (
	systemVersionPath = "System/Library/CoreServices/SystemVersion.plist"
	packagesPath      = "System/Installation/Packages"
)

// checkDisc holds the volume in the image at path to the pins of the
// disc whose build it reports: the SHA-256 of every file in its Packages
// directory. Dotfiles and subdirectories are skipped, as verify-disc.sh's
// "for f in *" with "[ -f ]" skipped them; a symbolic or hard link, which
// that script followed, is refused by name, as is a compressed file.
func checkDisc(ctx context.Context, rel Release, path string, onChunk func()) (Delivered, error) {
	img, err := image.OpenVolume(path)
	if err != nil {
		return Delivered{}, err
	}
	defer img.Close()
	vol, err := hfs.Open(img, img.Size)
	if err != nil {
		return Delivered{}, fmt.Errorf("%s: %w", path, err)
	}
	build, err := discBuild(vol)
	if err != nil {
		return Delivered{}, fmt.Errorf("%s: %w", path, err)
	}
	var disc *Disc
	var builds []string
	for i := range rel.Discs {
		builds = append(builds, rel.Discs[i].Build)
		if rel.Discs[i].Build == build {
			disc = &rel.Discs[i]
		}
	}
	if disc == nil {
		return Delivered{}, fmt.Errorf("build %s is not a disc this module knows for version %s (it knows %s)", build, rel.Name, strings.Join(builds, ", "))
	}
	ents, err := vol.ReadDir(packagesPath)
	if errors.Is(err, fs.ErrNotExist) {
		return Delivered{}, fmt.Errorf("no Packages directory on the disc at %s: %w", packagesPath, err)
	}
	if err != nil {
		return Delivered{}, fmt.Errorf("reading %s on the disc: %w", packagesPath, err)
	}
	got := map[string]string{}
	for _, e := range ents {
		if strings.HasPrefix(e.Name, ".") {
			continue
		}
		switch {
		case e.Kind == hfs.HardLink:
			return Delivered{}, fmt.Errorf("package %s is a hard link, which this reader does not follow", e.Name)
		case e.Kind == hfs.Symlink:
			return Delivered{}, fmt.Errorf("package %s is a symbolic link, which this reader does not follow", e.Name)
		case e.Kind != hfs.File:
			continue
		case e.Compressed:
			return Delivered{}, fmt.Errorf("package %s is compressed (HFS+ compression), so its bytes are not the pinned ones to read", e.Name)
		}
		r, _, err := vol.Open(packagesPath + "/" + e.Name)
		if err != nil {
			return Delivered{}, fmt.Errorf("package %s: %w", e.Name, err)
		}
		sum, err := hashReader(ctx, r, onChunk)
		if err != nil {
			return Delivered{}, fmt.Errorf("checking %s in %s: %w", e.Name, path, err)
		}
		got[e.Name] = sum
	}
	var problems []string
	for name, want := range disc.Sums {
		switch g, ok := got[name]; {
		case !ok:
			problems = append(problems, name+": missing from the disc")
		case g != want:
			problems = append(problems, fmt.Sprintf("%s: sha256 %s is not the pinned %s", name, g, want))
		}
	}
	for name := range got {
		if _, ok := disc.Sums[name]; !ok {
			problems = append(problems, name+": on the disc but not pinned")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return Delivered{}, &CheckError{Release: rel.Name, Build: build, Problems: problems}
	}
	return Delivered{Release: rel.Name, Build: build, ContentDigest: DiscDigest(*disc), Path: path}, nil
}

// discBuild is the ProductBuildVersion in the volume's SystemVersion.plist:
// the <string> after that <key>.
func discBuild(vol *hfs.Volume) (string, error) {
	r, _, err := vol.Open(systemVersionPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("no SystemVersion.plist on the disc: is it a Mac OS X install disc?")
	}
	if err != nil {
		return "", fmt.Errorf("SystemVersion.plist: %w", err)
	}
	dec := xml.NewDecoder(io.LimitReader(r, 1<<20))
	dec.Strict = false
	var key, build string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var text string
		if se.Name.Local == "key" || se.Name.Local == "string" {
			if err := dec.DecodeElement(&text, &se); err != nil {
				break
			}
		}
		switch {
		case se.Name.Local == "key":
			key = text
		case se.Name.Local == "string" && key == "ProductBuildVersion":
			build = strings.TrimSpace(text)
		}
		if build != "" {
			break
		}
	}
	if build == "" {
		return "", errors.New("SystemVersion.plist names no build: is it a Mac OS X install disc?")
	}
	return build, nil
}
