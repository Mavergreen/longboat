package installmedia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, name string, b []byte) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return path, hex.EncodeToString(s[:])
}

// fixtureContent spans more than one 4 MiB read, so the hash is
// assembled from several chunks.
func fixtureContent() []byte {
	b := make([]byte, 9<<20+17)
	for i := range b {
		b[i] = byte(i*7 + i>>13)
	}
	return b
}

func TestWholeFileCheck(t *testing.T) {
	content := fixtureContent()
	path, sum := writeFixture(t, "InstallESD.dmg", content)
	rel := Release{Name: "10.9", Codename: "mavericks", FileName: "InstallESD.dmg",
		Whole: &WholeFile{Name: "fixture", URL: "http://example.invalid/x", SHA256: sum, Build: "13ZFIX"}}

	got, err := Check(context.Background(), rel, path)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Delivered{Release: "10.9", Build: "13ZFIX", ContentDigest: sum, Path: path}); got != want {
		t.Errorf("Check = %+v, want %+v", got, want)
	}

	content[len(content)-1] ^= 1
	other, otherSum := writeFixture(t, "InstallESD.dmg", content)
	_, err = Check(context.Background(), rel, other)
	if err == nil {
		t.Fatal("Check passed a file that is not the pinned one")
	}
	for _, want := range []string{"not the pinned", other, otherSum, sum} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestWholeFileCheckOfAMissingFile(t *testing.T) {
	rel := Release{Name: "10.9", Whole: &WholeFile{SHA256: strings.Repeat("0", 64), Build: "13ZFIX"}}
	missing := filepath.Join(t.TempDir(), "absent.dmg")
	if _, err := Check(context.Background(), rel, missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("Check of a missing file returned %v", err)
	}
}

func TestCheckStopsWhenCancelled(t *testing.T) {
	path, sum := writeFixture(t, "InstallESD.dmg", fixtureContent())
	rel := Release{Name: "10.9", Whole: &WholeFile{SHA256: sum, Build: "13ZFIX"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Check(ctx, rel, path)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Check under a cancelled context returned %v, want context.Canceled", err)
	}
}

func TestCheckStopsBetweenChunks(t *testing.T) {
	path, sum := writeFixture(t, "InstallESD.dmg", fixtureContent())
	rel := Release{Name: "10.9", Whole: &WholeFile{SHA256: sum, Build: "13ZFIX"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	_, err := check(ctx, rel, path, func() {
		reads++
		if reads == 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancel after the first chunk returned %v, want context.Canceled", err)
	}
	if reads != 2 {
		t.Errorf("Check read %d chunks after a cancel during the second, want it to stop there", reads)
	}
}

func TestTenSixHasNoContentCheckYet(t *testing.T) {
	sl, err := Lookup("10.6")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := writeFixture(t, "install-disc.img", []byte("not a disc"))
	_, err = Check(context.Background(), sl, path)
	if err == nil || err.Error() != "release 10.6: no content check yet" {
		t.Errorf("Check(10.6) returned %v", err)
	}
}

func TestCheckErrorNamesTheBuildAndEveryProblem(t *testing.T) {
	err := error(&CheckError{Release: "10.6", Build: "10A432", Problems: []string{"A.pkg: missing from the disc", "B.pkg: on the disc but not pinned"}})
	want := "the disc is not build 10A432 as it shipped: A.pkg: missing from the disc; B.pkg: on the disc but not pinned"
	if err.Error() != want {
		t.Errorf("CheckError = %q, want %q", err, want)
	}
}
