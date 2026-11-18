package safepath

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	good := []string{"core.zip", "ggml-large-v3-q5.bin", "a", "Whisper Large v3.bin", ".hidden", "ünïcode.txt", "file.tar.gz"}
	for _, n := range good {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v, want ok", n, err)
		}
	}
	bad := map[string]string{
		"":                       "empty",
		".":                      "not a file name",
		"..":                     "not a file name",
		"a/b":                    "not allowed",
		`a\b`:                    "not allowed",
		"C:evil":                 "not allowed",
		"file.txt:ads":           "not allowed",
		"a|b":                    "not allowed",
		"a?b":                    "not allowed",
		"a*b":                    "not allowed",
		"a\x00b":                 "control",
		"a\nb":                   "control",
		"trailing.":              "space or a dot",
		"trailing ":              "space or a dot",
		"CON":                    "reserved",
		"con":                    "reserved",
		"nul.txt":                "reserved",
		"Com1.log":               "reserved",
		"LPT9":                   "reserved",
		"\xff\xfe":               "UTF-8",
		strings.Repeat("a", 256): "longer",
	}
	for n, want := range bad {
		err := ValidName(n)
		if err == nil {
			t.Errorf("ValidName(%q) accepted a bad name", n)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidName(%q) = %v, want it to mention %q", n, err, want)
		}
	}
	// A name that only starts like a device name is fine.
	if err := ValidName("console.txt"); err != nil {
		t.Errorf("console.txt: %v", err)
	}
}

func TestCleanRel(t *testing.T) {
	got, err := CleanRel("bin/tools/a.dll")
	if err != nil || got != filepath.Join("bin", "tools", "a.dll") {
		t.Fatalf("CleanRel = %q, %v", got, err)
	}
	got, err = CleanRel("bin/")
	if err != nil || got != "bin" {
		t.Fatalf("a folder entry: %q, %v", got, err)
	}
	for _, p := range []string{"", "/", "/etc/passwd", "../x", "a/../b", "a/./b", "a//b", `a\b`, "C:/x", "bin/CON", "a/b/.."} {
		if _, err := CleanRel(p); err == nil {
			t.Errorf("CleanRel(%q) accepted a bad path", p)
		}
	}
}
