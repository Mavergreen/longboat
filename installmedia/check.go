package installmedia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
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
		return Delivered{}, fmt.Errorf("release %s: no content check yet", rel.Name)
	}
	f, err := os.Open(path)
	if err != nil {
		return Delivered{}, err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, chunk)
	for {
		if onChunk != nil {
			onChunk()
		}
		if err := ctx.Err(); err != nil {
			return Delivered{}, fmt.Errorf("checking %s: %w", path, err)
		}
		n, err := io.ReadFull(f, buf)
		h.Write(buf[:n])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return Delivered{}, err
		}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if sum != rel.Whole.SHA256 {
		return Delivered{}, fmt.Errorf("%s has sha256 %s, not the pinned %s", path, sum, rel.Whole.SHA256)
	}
	return Delivered{Release: rel.Name, Build: rel.Whole.Build, ContentDigest: sum, Path: path}, nil
}
