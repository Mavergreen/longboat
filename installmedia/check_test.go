package installmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mavergreen/longboat/installmedia/internal/fixture"
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

func TestCheckErrorNamesTheBuildAndEveryProblem(t *testing.T) {
	err := error(&CheckError{Release: "10.6", Build: "10A432", Problems: []string{"A.pkg: missing from the disc", "B.pkg: on the disc but not pinned"}})
	want := "the disc is not build 10A432 as it shipped: A.pkg: missing from the disc; B.pkg: on the disc but not pinned"
	if err.Error() != want {
		t.Errorf("CheckError = %q, want %q", err, want)
	}
}

// discPkgs are the fixture disc's packages; two are big enough to take
// more than one read.
func discPkgs() map[string][]byte {
	return map[string][]byte{
		"BaseSystem.pkg": []byte("base system bytes"),
		"Big.pkg":        fixtureContent(),
		"Small.pkg":      []byte("small"),
	}
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func fixtureDisc(pkgs map[string][]byte) Disc {
	d := Disc{Build: "10ZFIX", Sums: map[string]string{}}
	for n, b := range pkgs {
		d.Sums[n] = sumOf(b)
	}
	return d
}

func fixtureRelease(pkgs map[string][]byte) Release {
	return Release{Name: "10.6", Codename: "snowleopard", FileName: "install-disc.img", Discs: []Disc{fixtureDisc(pkgs)}}
}

func writeImage(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "install-disc.img")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func discImage(t *testing.T, build string, pkgs map[string][]byte, extra []fixture.File) string {
	t.Helper()
	return writeImage(t, fixture.HFS(fixture.SnowLeopardDisc(build, pkgs, extra), fixture.HFSOptions{}))
}

func TestTheFixtureDiscPasses(t *testing.T) {
	pkgs := discPkgs()
	rel := fixtureRelease(pkgs)
	path := discImage(t, "10ZFIX", pkgs, nil)
	got, err := Check(context.Background(), rel, path)
	if err != nil {
		t.Fatal(err)
	}
	want := Delivered{Release: "10.6", Build: "10ZFIX", ContentDigest: DiscDigest(rel.Discs[0]), Path: path}
	if got != want {
		t.Errorf("Check = %+v, want %+v", got, want)
	}
}

func TestEveryContainerOfTheSameDiscPasses(t *testing.T) {
	// Small packages: the bzip2 fixture is slow over megabytes.
	pkgs := discPkgs()
	pkgs["Big.pkg"] = bytes.Repeat([]byte("big package "), 9000)
	rel := fixtureRelease(pkgs)
	vol := fixture.HFS(fixture.SnowLeopardDisc("10ZFIX", pkgs, nil), fixture.HFSOptions{})
	images := map[string][]byte{
		"bare":       vol,
		"APM":        fixture.APM(vol),
		"GPT":        fixture.GPT(vol),
		"UDIF-zlib":  fixture.UDIF(fixture.APM(vol), fixture.Zlib, 64),
		"UDIF-bzip2": fixture.UDIF(fixture.GPT(vol), fixture.Bzip2, 256),
	}
	var first Delivered
	for name, b := range images {
		path := writeImage(t, b)
		got, err := Check(context.Background(), rel, path)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got.Path = ""
		if first == (Delivered{}) {
			first = got
		}
		if got != first {
			t.Errorf("%s: Check = %+v, others gave %+v", name, got, first)
		}
	}
	if first.Build != "10ZFIX" {
		t.Errorf("Check gave %+v", first)
	}
}

func TestDotfilesInPackagesAreIgnored(t *testing.T) {
	pkgs := discPkgs()
	extra := []fixture.File{
		{Path: "System/Installation/Packages/.DS_Store", Data: []byte("junk")},
		{Path: "System/Installation/Packages/._Small.pkg", Data: []byte("fork")},
	}
	if _, err := Check(context.Background(), fixtureRelease(pkgs), discImage(t, "10ZFIX", pkgs, extra)); err != nil {
		t.Error(err)
	}
}

func TestSubdirectoriesInPackagesAreIgnored(t *testing.T) {
	pkgs := discPkgs()
	extra := []fixture.File{
		{Path: "System/Installation/Packages/Extras/inner.pkg", Data: []byte("inner")},
		{Path: "System/Installation/Packages/Empty/"},
	}
	if _, err := Check(context.Background(), fixtureRelease(pkgs), discImage(t, "10ZFIX", pkgs, extra)); err != nil {
		t.Error(err)
	}
}

func TestAnotherBuildIsRefusedByName(t *testing.T) {
	pkgs := discPkgs()
	_, err := Check(context.Background(), fixtureRelease(pkgs), discImage(t, "10A999", pkgs, nil))
	if err == nil {
		t.Fatal("Check passed a disc of another build")
	}
	for _, want := range []string{"10A999", "10ZFIX"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestPackageProblemsAreAllNamed(t *testing.T) {
	pkgs := discPkgs()
	rel := fixtureRelease(pkgs)
	onDisc := discPkgs()
	delete(onDisc, "Small.pkg")
	onDisc["BaseSystem.pkg"] = []byte("changed")
	onDisc["Extra.pkg"] = []byte("extra")
	_, err := Check(context.Background(), rel, discImage(t, "10ZFIX", onDisc, nil))
	var ce *CheckError
	if !errors.As(err, &ce) {
		t.Fatalf("Check returned %v, want a *CheckError", err)
	}
	want := []string{
		"BaseSystem.pkg: sha256 " + sumOf([]byte("changed")) + " is not the pinned " + sumOf([]byte("base system bytes")),
		"Extra.pkg: on the disc but not pinned",
		"Small.pkg: missing from the disc",
	}
	if ce.Release != "10.6" || ce.Build != "10ZFIX" || strings.Join(ce.Problems, "\n") != strings.Join(want, "\n") {
		t.Errorf("CheckError = %+v, want problems %q", ce, want)
	}
}

func TestNoSystemVersionPlist(t *testing.T) {
	vol := fixture.HFS([]fixture.File{{Path: "System/Installation/Packages/A.pkg", Data: []byte("a")}}, fixture.HFSOptions{})
	_, err := Check(context.Background(), fixtureRelease(discPkgs()), writeImage(t, vol))
	if err == nil || !strings.Contains(err.Error(), "is it a Mac OS X install disc?") {
		t.Errorf("Check returned %v", err)
	}
}

func TestNoPackagesDirectory(t *testing.T) {
	vol := fixture.HFS(fixture.SnowLeopardDisc("10ZFIX", nil, nil)[:1], fixture.HFSOptions{})
	_, err := Check(context.Background(), fixtureRelease(discPkgs()), writeImage(t, vol))
	if err == nil || !strings.Contains(err.Error(), "Packages") {
		t.Errorf("Check returned %v", err)
	}
}

func TestACompressedPackageIsRefused(t *testing.T) {
	pkgs := discPkgs()
	extra := []fixture.File{{Path: "System/Installation/Packages/Packed.pkg", Compressed: true}}
	_, err := Check(context.Background(), fixtureRelease(pkgs), discImage(t, "10ZFIX", pkgs, extra))
	if err == nil || !strings.Contains(err.Error(), "Packed.pkg") || !strings.Contains(err.Error(), "compressed") {
		t.Errorf("Check returned %v, want an error naming Packed.pkg and compressed", err)
	}
}

func TestAHardLinkedPackageIsRefused(t *testing.T) {
	pkgs := discPkgs()
	extra := []fixture.File{{Path: "System/Installation/Packages/Linked.pkg", HardLink: true}}
	_, err := Check(context.Background(), fixtureRelease(pkgs), discImage(t, "10ZFIX", pkgs, extra))
	if err == nil || !strings.Contains(err.Error(), "Linked.pkg") || !strings.Contains(err.Error(), "hard link") {
		t.Errorf("Check returned %v, want an error naming Linked.pkg and hard link", err)
	}
}

func TestASymlinkedPackageIsRefused(t *testing.T) {
	pkgs := discPkgs()
	extra := []fixture.File{{Path: "System/Installation/Packages/link.pkg", Symlink: "Small.pkg"}}
	_, err := Check(context.Background(), fixtureRelease(pkgs), discImage(t, "10ZFIX", pkgs, extra))
	if err == nil || !strings.Contains(err.Error(), "link.pkg") || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("Check returned %v, want an error naming link.pkg and symbolic link", err)
	}
}

func TestASymlinkedPackagesDirectoryIsRefused(t *testing.T) {
	vol := fixture.HFS([]fixture.File{
		fixture.SnowLeopardDisc("10ZFIX", nil, nil)[0],
		{Path: "System/Installation/Packages", Symlink: "/elsewhere"},
	}, fixture.HFSOptions{})
	_, err := Check(context.Background(), fixtureRelease(discPkgs()), writeImage(t, vol))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") || strings.Contains(err.Error(), "no Packages directory") {
		t.Errorf("Check returned %v, want a refusal naming a symbolic link, not a missing directory", err)
	}
}

func TestADiscCheckStopsBetweenChunks(t *testing.T) {
	pkgs := discPkgs()
	path := discImage(t, "10ZFIX", pkgs, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	_, err := check(ctx, fixtureRelease(pkgs), path, func() {
		reads++
		if reads == 3 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancel during the hashing returned %v, want context.Canceled", err)
	}
	if reads != 3 {
		t.Errorf("Check read %d chunks, want it to stop at the third", reads)
	}
}

func TestADamagedImageIsRefused(t *testing.T) {
	path := writeImage(t, []byte("not a disc"))
	_, err := Check(context.Background(), fixtureRelease(discPkgs()), path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "no Apple Partition Map") {
		t.Errorf("Check of a file that is no disc image returned %v", err)
	}
}

func TestTheRealTenSixReleaseRefusesAFixture(t *testing.T) {
	sl, err := Lookup("10.6")
	if err != nil {
		t.Fatal(err)
	}
	pkgs := discPkgs()
	_, err = Check(context.Background(), sl, discImage(t, "10ZFIX", pkgs, nil))
	if err == nil || !strings.Contains(err.Error(), "10ZFIX") {
		t.Errorf("Check(10.6) of a fixture returned %v, want a refusal naming build 10ZFIX", err)
	}
}
