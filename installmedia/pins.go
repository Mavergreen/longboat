package installmedia

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed pins/*
var pins embed.FS

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// parseWholeFile parses pins/installesd-10.9.tsv: "#" lines are
// commentary, and its one row is name, url, sha256 and build, separated
// by tabs.
func parseWholeFile(b []byte) (WholeFile, error) {
	var rows []WholeFile
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 || f[0] == "" || f[1] == "" || !isSHA256(f[2]) || f[3] == "" {
			return WholeFile{}, fmt.Errorf("installesd-10.9.tsv: %q is not \"<name>\\t<url>\\t<sha256>\\t<build>\"", line)
		}
		rows = append(rows, WholeFile{Name: f[0], URL: f[1], SHA256: f[2], Build: f[3]})
	}
	if len(rows) != 1 {
		return WholeFile{}, fmt.Errorf("installesd-10.9.tsv: has %d rows, want 1", len(rows))
	}
	return rows[0], nil
}

// parseDiscPins parses pins/snowleopard-packages.sha256: a
// "# build <build>" line opens each disc's section, "<sha256>  <name>"
// lines fill it, and every other "#" line is commentary.
func parseDiscPins(b []byte) ([]Disc, error) {
	var discs []Disc
	for _, line := range strings.Split(string(b), "\n") {
		if build, ok := strings.CutPrefix(line, "# build "); ok && build != "" && !strings.Contains(build, " ") {
			for _, d := range discs {
				if d.Build == build {
					return nil, fmt.Errorf("snowleopard-packages.sha256: build %s has two sections", build)
				}
			}
			discs = append(discs, Disc{Build: build, Sums: map[string]string{}})
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || name == "" || !isSHA256(sum) || len(discs) == 0 {
			return nil, fmt.Errorf("snowleopard-packages.sha256: %q is not \"<sha256>  <name>\" under a \"# build\" line", line)
		}
		d := discs[len(discs)-1]
		if _, dup := d.Sums[name]; dup {
			return nil, fmt.Errorf("snowleopard-packages.sha256: %s is pinned twice under build %s", name, d.Build)
		}
		d.Sums[name] = sum
	}
	return discs, nil
}
