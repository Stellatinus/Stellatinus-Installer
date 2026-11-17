package checks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
)

func TestPrivileges(t *testing.T) {
	if r := Privileges(manifest.ScopeUser, false); !r.Passed() {
		t.Fatalf("per-user without admin rights should pass: %+v", r)
	}
	if r := Privileges(manifest.ScopeMachine, false); r.Passed() || r.Fix != FixElevate {
		t.Fatalf("per-machine without admin rights must fail and offer to elevate: %+v", r)
	}
	if r := Privileges(manifest.ScopeMachine, true); !r.Passed() {
		t.Fatalf("per-machine with admin rights should pass: %+v", r)
	}
}

func TestWriteAccessCreatesAndRemovesATestFile(t *testing.T) {
	dir := t.TempDir()
	r := WriteAccess("install", dir)
	if !r.Passed() {
		t.Fatalf("%+v", r)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the test file was left behind: %v", entries)
	}
}

func TestWriteAccessDoesNotCreateTheFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if r := WriteAccess("install", dir); !r.Passed() {
		t.Fatalf("a folder that can be created should pass: %+v", r)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the check created the folder")
	}
}

func TestWriteAccessFailsOnAFileAndOnRelativePaths(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := WriteAccess("output", f)
	if r.Passed() || r.GoBack != "output" || r.Fix != FixChooseFolder {
		t.Fatalf("a file must fail and go back to its step: %+v", r)
	}
	if r := WriteAccess("output", "relative/path"); r.Passed() {
		t.Fatalf("a relative path must fail: %+v", r)
	}
}

func TestWriteAccessFailsInAReadOnlyFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not apply")
	}
	if Elevated() {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	r := WriteAccess("output", dir)
	if r.Passed() || !strings.Contains(r.Detail, "permission denied") {
		t.Fatalf("a read-only folder must fail with a clear reason: %+v", r)
	}
}

func TestReadAccess(t *testing.T) {
	if r := ReadAccess("models", t.TempDir()); !r.Passed() {
		t.Fatalf("%+v", r)
	}
	if r := ReadAccess("models", filepath.Join(t.TempDir(), "missing")); r.Passed() {
		t.Fatalf("a missing folder must fail: %+v", r)
	}
}

func TestFreeSpace(t *testing.T) {
	dir := t.TempDir()
	if r := FreeSpace("install", dir, 1); !r.Passed() {
		t.Fatalf("1 byte should fit: %+v", r)
	}
	r := FreeSpace("install", dir, 1<<60)
	if r.Passed() || r.GoBack != "install" {
		t.Fatalf("an exabyte must not fit, and must go back to the step: %+v", r)
	}
}

func TestOverlap(t *testing.T) {
	root := t.TempDir()
	p := func(s string) string { return filepath.Join(root, s) }
	order := []string{"install", "models", "output"}
	if r := Overlap(map[string]string{"install": p("a"), "models": p("b"), "output": p("c")}, order); !r.Passed() {
		t.Fatalf("separate folders should pass: %+v", r)
	}
	r := Overlap(map[string]string{"install": p("a"), "models": p("a/models"), "output": p("c")}, order)
	if r.Passed() || r.GoBack != "models" {
		t.Fatalf("a folder inside another must fail and go back to the later step: %+v", r)
	}
	if r := Overlap(map[string]string{"install": p("a"), "output": p("a")}, order); r.Passed() {
		t.Fatalf("the same folder twice must fail: %+v", r)
	}
	// "a" and "ab" are siblings, not nested.
	if r := Overlap(map[string]string{"install": p("a"), "models": p("ab")}, order); !r.Passed() {
		t.Fatalf("a prefix of the name is not nesting: %+v", r)
	}
}

func TestExisting(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "record.json")
	if r := Existing(rec); r.Status != OK {
		t.Fatalf("%+v", r)
	}
	if err := os.WriteFile(rec, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Existing(rec); r.Status != Warning || !r.Passed() {
		t.Fatalf("an existing record is a warning that does not block: %+v", r)
	}
}

func TestRunWithTheHaedusExample(t *testing.T) {
	m, err := manifest.Load("../../examples/haedus.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := map[string]string{
		"install": filepath.Join(root, "program"),
		"models":  filepath.Join(root, "models"),
		"output":  filepath.Join(root, "out"),
	}
	// The example needs about 1.4 GB; skip the space part on a tiny disk.
	results := Run(m, paths, Options{Scope: manifest.ScopeUser, RecordPath: filepath.Join(root, "record.json")})
	var ids []string
	for _, r := range results {
		ids = append(ids, r.ID)
		if r.ID == "space:install" || r.ID == "space:models" {
			continue
		}
		if !r.Passed() {
			t.Errorf("%s failed: %s", r.ID, r.Detail)
		}
	}
	want := []string{"privileges", "write:install", "space:install", "write:models", "space:models", "write:output", "overlap", "existing"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("checks in order = %v, want %v", ids, want)
	}
	for _, d := range paths {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("the checks created %s", d)
		}
	}
}

func TestRunReportsNestedFolders(t *testing.T) {
	m, err := manifest.Load("../../examples/haedus.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	results := Run(m, map[string]string{
		"install": filepath.Join(root, "program"),
		"models":  filepath.Join(root, "program", "models"),
		"output":  filepath.Join(root, "out"),
	}, Options{})
	for _, r := range results {
		if r.ID == "overlap" {
			if r.Passed() || r.GoBack != "models" {
				t.Fatalf("overlap not reported: %+v", r)
			}
			return
		}
	}
	t.Fatal("no overlap result")
}

func TestAnAdoptedModelsFolderIsOnlyReadWhenNothingIsDownloadedIntoIt(t *testing.T) {
	m, err := manifest.Load("../../examples/haedus.json")
	if err != nil {
		t.Fatal(err)
	}
	m.Components = nil
	root := t.TempDir()
	models := filepath.Join(root, "models")
	if err := os.Mkdir(models, 0o700); err != nil {
		t.Fatal(err)
	}
	results := Run(m, map[string]string{"install": filepath.Join(root, "p"), "models": models, "output": filepath.Join(root, "o")}, Options{})
	var got []string
	for _, r := range results {
		got = append(got, r.ID)
	}
	if !strings.Contains(strings.Join(got, ","), "read:models") || strings.Contains(strings.Join(got, ","), "write:models") {
		t.Fatalf("adopted models with nothing to download should be read, not written: %v", got)
	}
}

func TestFormatBytes(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 1 << 30: "1.0 GB"} {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
