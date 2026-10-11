package cli

import (
	"path"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxNameBytes is the longest file name saved: 255 bytes is what the
// common file systems allow (APFS, ext4; NTFS counts 255 UTF-16 units,
// which 255 bytes never exceed).
const maxNameBytes = 255

// diskName is the name a shared file is saved under on this system.
func diskName(name string) string {
	return diskNameFor(name, runtime.GOOS == "windows")
}

// diskNameFor is the name a shared file, named by its sender, is saved
// under: never outside the output directory, nothing the system would read
// as a device or a stream, no characters a file name can't hold or that
// change how it shows, and no longer than maxNameBytes. windows adds
// Windows' rules, so that they are tested everywhere.
func diskNameFor(name string, windows bool) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(path.Clean("/" + name))
	name = strings.Map(func(r rune) rune {
		switch {
		case r == unicode.ReplacementChar, unicode.IsControl(r), hidden(r):
			return '_'
		case windows && strings.ContainsRune(`<>:"|?*`, r):
			return '_'
		}
		return r
	}, name)
	if windows {
		// Windows drops trailing dots and spaces, so a.txt. would be a.txt.
		name = strings.TrimRight(name, ". ")
		if reservedOnWindows(name) {
			name = "_" + name
		}
	}
	if name == "" || name == "/" || name == "." || name == ".." {
		name = "file"
	}
	name = fitName(name, "")
	if windows {
		if name = strings.TrimRight(name, ". "); name == "" {
			name = "file"
		}
	}
	return name
}

// numbered is a name with " (n)" before its extension, for the n-th file
// of a bundle that would be saved under the same name.
func numbered(name string, n int) string {
	return fitName(name, " ("+strconv.Itoa(n)+")")
}

// fitName puts suffix before the name's extension and shortens the rest,
// at a character boundary, to keep it within maxNameBytes. An extension
// longer than a few characters counts as part of the name.
func fitName(name, suffix string) string {
	ext := path.Ext(name)
	if len(ext) > 16 || ext == name {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	room := maxNameBytes - len(suffix) - len(ext)
	for len(stem) > room {
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	return stem + suffix + ext
}

// reservedOnWindows reports whether Windows reads the name as a device,
// as Go's own check does (internal/filepathlite, isReservedName): CON,
// PRN, AUX, NUL, COM and LPT with a digit or ¹²³, CONIN$ and CONOUT$,
// whatever follows a dot or a colon and with trailing spaces. Older
// Windows reserve them with an extension too, so these count regardless.
func reservedOnWindows(name string) bool {
	base := name
	if i := strings.IndexAny(base, ".:"); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if rest, ok := strings.CutPrefix(base, "COM"); ok {
		return portNumber(rest)
	}
	if rest, ok := strings.CutPrefix(base, "LPT"); ok {
		return portNumber(rest)
	}
	return false
}

func portNumber(s string) bool {
	return len(s) == 1 && '0' <= s[0] && s[0] <= '9' || s == "\u00b9" || s == "\u00b2" || s == "\u00b3"
}
