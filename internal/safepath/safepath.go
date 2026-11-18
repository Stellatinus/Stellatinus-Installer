// Package safepath checks file names and relative paths that come from
// outside the program: a manifest, or the entries of a downloaded archive. A
// name that passes is safe to join under a folder on Windows, macOS and Linux
// alike, so a package built on one system installs the same on another. It
// does not touch the disk.
package safepath

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxName = 255

// reserved are device names Windows treats specially in any folder, with or
// without an extension. They are refused on every system.
var reserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidName checks one file or folder name (not a path).
func ValidName(name string) error {
	switch {
	case name == "":
		return errors.New("the name is empty")
	case !utf8.ValidString(name):
		return errors.New("the name is not valid UTF-8")
	case len(name) > maxName:
		return fmt.Errorf("the name is longer than %d bytes", maxName)
	case name == "." || name == "..":
		return fmt.Errorf("%q is not a file name", name)
	case strings.HasSuffix(name, " ") || strings.HasSuffix(name, "."):
		return fmt.Errorf("%q ends with a space or a dot, which Windows does not keep", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%q contains a control character", name)
		}
		if strings.ContainsRune(`/\:<>"|?*`, r) {
			return fmt.Errorf("%q contains the character %q, which is not allowed in a name", name, r)
		}
	}
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if reserved[strings.ToLower(base)] {
		return fmt.Errorf("%q is a reserved device name on Windows", name)
	}
	return nil
}

// CleanRel checks a relative path written with "/" (a zip entry, or a
// sub-folder in a manifest) and returns it with the system's separators. It
// refuses an absolute path, a drive letter, a backslash, an empty segment, "."
// and "..". One trailing "/" (a folder entry) is accepted and dropped.
func CleanRel(p string) (string, error) {
	if p == "" {
		return "", errors.New("the path is empty")
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%q is an absolute path", p+"/")
	}
	parts := strings.Split(p, "/")
	for _, s := range parts {
		if err := ValidName(s); err != nil {
			return "", fmt.Errorf("path %q: %w", p, err)
		}
	}
	return filepath.Join(parts...), nil
}
