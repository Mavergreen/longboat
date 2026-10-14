package installmedia

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestLookupKnowsBothReleases(t *testing.T) {
	mav, err := Lookup("10.9")
	if err != nil {
		t.Fatal(err)
	}
	if mav.Name != "10.9" || mav.Codename != "mavericks" || mav.FileName != "InstallESD.dmg" {
		t.Errorf("10.9 is %q %q %q, want 10.9 mavericks InstallESD.dmg", mav.Name, mav.Codename, mav.FileName)
	}
	if mav.Whole == nil {
		t.Fatal("10.9 has no whole-file pin")
	}
	if want := "c861fd59e82bf777496809a0d2a9b58f66691ee56738031f55874a3fe1d7c3ff"; mav.Whole.SHA256 != want {
		t.Errorf("10.9's sha256 is %s, want %s", mav.Whole.SHA256, want)
	}
	if mav.Whole.Name != "apple-installesd-10.9.5" || mav.Whole.Build != "13F34" {
		t.Errorf("10.9's pin is %q build %q, want apple-installesd-10.9.5 build 13F34", mav.Whole.Name, mav.Whole.Build)
	}
	if !strings.HasSuffix(mav.Whole.URL, "/InstallESD.dmg") {
		t.Errorf("10.9's URL %q does not name InstallESD.dmg", mav.Whole.URL)
	}
	if len(mav.Discs) != 0 {
		t.Errorf("10.9 has %d disc pins, want none", len(mav.Discs))
	}

	sl, err := Lookup("10.6")
	if err != nil {
		t.Fatal(err)
	}
	if sl.Name != "10.6" || sl.Codename != "snowleopard" || sl.FileName != "install-disc.img" {
		t.Errorf("10.6 is %q %q %q, want 10.6 snowleopard install-disc.img", sl.Name, sl.Codename, sl.FileName)
	}
	if sl.Whole != nil {
		t.Errorf("10.6 has a whole-file pin %+v, want none: every faithful copy of a disc differs as a file", *sl.Whole)
	}
	if len(sl.Discs) != 1 {
		t.Fatalf("10.6 has %d disc pins, want 1", len(sl.Discs))
	}
	if d := sl.Discs[0]; d.Build != "10A432" || len(d.Sums) != 281 {
		t.Errorf("10.6's disc is build %q with %d sums, want 10A432 with 281", d.Build, len(d.Sums))
	}
	if got, want := sl.Discs[0].Sums["BSD.pkg"], "b77899deb8c8da2edef18b5c8dcb51979a27bf22e333a6a9fd5d41e5fe8e9e4f"; got != want {
		t.Errorf("10.6's BSD.pkg is pinned %q, want %s", got, want)
	}
	// Computed outside Go on 2026-10-14:
	// grep -E '^[0-9a-f]{64}  ' pins/snowleopard-packages.sha256 | LC_ALL=C sort | sha256sum
	if got, want := DiscDigest(sl.Discs[0]), "d5e325e5966539262dcc754446733fbeb3f0424b0d73ae0277f49f860a381c67"; got != want {
		t.Errorf("10.6's disc digest is %s, want %s", got, want)
	}
}

func TestReleasesAreTenNineThenTenSix(t *testing.T) {
	var names []string
	for _, r := range Releases() {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, " "); got != "10.9 10.6" {
		t.Errorf("Releases() is %q, want \"10.9 10.6\"", got)
	}
}

func TestReleasesAreCopies(t *testing.T) {
	a, _ := Lookup("10.6")
	a.Discs[0].Sums["BSD.pkg"] = "tampered"
	a.Discs[0].Build = "tampered"
	b, _ := Lookup("10.6")
	if b.Discs[0].Sums["BSD.pkg"] == "tampered" || b.Discs[0].Build == "tampered" {
		t.Error("changing one Lookup's result changed the next one's: callers share the pins")
	}
	c, _ := Lookup("10.9")
	c.Whole.SHA256 = "tampered"
	d, _ := Lookup("10.9")
	if d.Whole.SHA256 == "tampered" {
		t.Error("changing one Lookup's whole-file pin changed the next one's")
	}
}

func TestLookupRefusesAnUnknownRelease(t *testing.T) {
	_, err := Lookup("10.5")
	if err == nil {
		t.Fatal("Lookup(10.5) succeeded")
	}
	for _, want := range []string{"10.5", "10.9", "10.6"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Lookup(10.5)'s error %q does not name %s", err, want)
		}
	}
}

func TestDiscDigestIsTheSortedPinLines(t *testing.T) {
	one := strings.Repeat("1", 64)
	two := strings.Repeat("2", 64)
	sum := sha256.Sum256([]byte(one + "  a\n" + two + "  b\n"))
	want := hex.EncodeToString(sum[:])
	if got := DiscDigest(Disc{Sums: map[string]string{"b": two, "a": one}}); got != want {
		t.Errorf("DiscDigest = %s, want %s", got, want)
	}
	if DiscDigest(Disc{Build: "other", Sums: map[string]string{"a": one, "b": two}}) != want {
		t.Error("DiscDigest depends on the build, not only on the pinned lines")
	}
}

func TestDiscPinsKeepTheirGrammar(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, in string
	}{
		{"a sum before any build", sum + "  A.pkg\n"},
		{"a line with one space", "# build 10A432\n" + sum + " A.pkg\n"},
		{"a line with no name", "# build 10A432\n" + sum + "\n"},
		{"a short sum", "# build 10A432\nabc  A.pkg\n"},
		{"an upper-case sum", "# build 10A432\n" + strings.ToUpper(sum) + "  A.pkg\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDiscPins([]byte(tc.in))
			if err == nil {
				t.Fatal("parsed")
			}
			if !strings.Contains(err.Error(), `is not "<sha256>  <name>" under a "# build" line`) {
				t.Errorf("error %q is not KnownDiscs's", err)
			}
		})
	}
	for _, in := range []string{
		"# build 10A432\n" + sum + "  A.pkg\n" + sum + "  A.pkg\n",
		"# build 10A432\n" + sum + "  A.pkg\n# build 10A432\n" + sum + "  B.pkg\n",
	} {
		if _, err := parseDiscPins([]byte(in)); err == nil || !strings.Contains(err.Error(), "twice") && !strings.Contains(err.Error(), "two sections") {
			t.Errorf("a pin file with a duplicate parsed, or failed with %v", err)
		}
	}
	discs, err := parseDiscPins([]byte("# commentary\n# build 10A432 (prose, not a section)\n#\n# build 10A432\n" +
		sum + "  A.pkg\n\n# build 10A433\n" + sum + "  Name With Spaces.pkg\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(discs) != 2 || discs[0].Build != "10A432" || discs[1].Build != "10A433" ||
		discs[0].Sums["A.pkg"] != sum || discs[1].Sums["Name With Spaces.pkg"] != sum {
		t.Errorf("parsed %+v", discs)
	}
}

func TestTheWholeFilePinIsOneWellFormedRow(t *testing.T) {
	sum := strings.Repeat("c", 64)
	good := "# comment\n\nname\thttp://example.invalid/x.dmg\t" + sum + "\t13F34\n"
	w, err := parseWholeFile([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if w != (WholeFile{Name: "name", URL: "http://example.invalid/x.dmg", SHA256: sum, Build: "13F34"}) {
		t.Errorf("parsed %+v", w)
	}
	for _, tc := range []struct{ name, in string }{
		{"no row", "# only commentary\n"},
		{"two rows", good + "other\thttp://example.invalid/y.dmg\t" + sum + "\t13F34\n"},
		{"three fields", "name\thttp://example.invalid/x.dmg\t" + sum + "\n"},
		{"an empty build", "name\thttp://example.invalid/x.dmg\t" + sum + "\t\n"},
		{"a short sum", "name\thttp://example.invalid/x.dmg\tabc\t13F34\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseWholeFile([]byte(tc.in)); err == nil {
				t.Fatal("parsed")
			} else if !strings.Contains(err.Error(), "installesd-10.9.tsv") {
				t.Errorf("error %q does not name the pin file", err)
			}
		})
	}
}

func TestTheTenNineReleaseUsesOsrecovery(t *testing.T) {
	mav, err := Lookup("10.9")
	if err != nil {
		t.Fatal(err)
	}
	if mav.Recipe == nil {
		t.Error("10.9 has no Recipe, want osrecovery's")
	}
	sl, err := Lookup("10.6")
	if err != nil {
		t.Fatal(err)
	}
	if sl.Recipe != nil {
		t.Error("10.6 has a Recipe, want none: its media is a disc the user images")
	}
}
