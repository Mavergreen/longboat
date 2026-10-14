package fixture

import "sort"

// SnowLeopardDisc is the files of a 10.6 install disc's volume as far as
// Check reads them: SystemVersion.plist naming build, and pkgs in
// System/Installation/Packages. extra files go in beside them, so a test
// can add a dotfile, a subdirectory or a compressed package.
func SnowLeopardDisc(build string, pkgs map[string][]byte, extra []File) []File {
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>ProductBuildVersion</key>
	<string>` + build + `</string>
	<key>ProductName</key>
	<string>Mac OS X</string>
</dict>
</plist>
`
	files := []File{{Path: "System/Library/CoreServices/SystemVersion.plist", Data: []byte(plist)}}
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		files = append(files, File{Path: "System/Installation/Packages/" + n, Data: pkgs[n]})
	}
	return append(files, extra...)
}
