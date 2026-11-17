package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

func do(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsageAndVersion(t *testing.T) {
	if code, _, _ := do(t); code != 2 {
		t.Fatalf("no arguments: exit %d, want 2", code)
	}
	if code, _, _ := do(t, "frobnicate"); code != 2 {
		t.Fatalf("unknown command: exit %d, want 2", code)
	}
	if code, out, _ := do(t, "version"); code != 0 || !strings.Contains(out, "stellatinus-installer") {
		t.Fatalf("version: %d %q", code, out)
	}
}

func TestValidate(t *testing.T) {
	code, out, _ := do(t, "validate", "../../examples/haedus.json")
	if code != 0 || !strings.Contains(out, "valid") {
		t.Fatalf("example should validate: %d %q", code, out)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schema_version": 1, "app": {"id": "X"}, "locations": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := do(t, "validate", bad)
	if code != 1 || !strings.Contains(errOut, "problem(s)") || !strings.Contains(errOut, "app.id") {
		t.Fatalf("a bad manifest must exit 1 and list the problems: %d %q", code, errOut)
	}
}

func TestAccent(t *testing.T) {
	code, out, _ := do(t, "accent", "#f2c200")
	if code != 0 || !strings.Contains(out, "light") || !strings.Contains(out, "dark") {
		t.Fatalf("%d %q", code, out)
	}
	if code, _, _ := do(t, "accent", "yellow"); code != 1 {
		t.Fatalf("a bad colour should exit 1, got %d", code)
	}
}

func TestCheckPassesAndFails(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "program")
	if code, out, errOut := do(t, "check", "--set", "install="+good, "../../examples/minimal.json"); code != 0 {
		t.Fatalf("check should pass: %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(good); err == nil {
		t.Fatal("check created the folder")
	}
	// A path that is a file must fail and name the step to go back to.
	f := filepath.Join(root, "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := do(t, "check", "--set", "install="+f, "../../examples/minimal.json")
	if code != 1 || !strings.Contains(out, "FAIL") || !strings.Contains(out, `"install"`) {
		t.Fatalf("a file as install folder must fail and point back to the step: %d\n%s", code, out)
	}
	if code, _, _ := do(t, "check", "--set", "nope=/tmp", "../../examples/minimal.json"); code != 2 {
		t.Fatalf("an unknown location in --set is a usage error, got %d", code)
	}
	if code, _, _ := do(t, "check", "--scope", "everyone", "../../examples/minimal.json"); code != 2 {
		t.Fatalf("a bad scope is a usage error, got %d", code)
	}
}

func TestPlanShowsWithoutRemoving(t *testing.T) {
	root := t.TempDir()
	models := filepath.Join(root, "models")
	mine, theirs := filepath.Join(models, "mine.bin"), filepath.Join(models, "theirs.bin")
	for _, p := range []string{mine, theirs} {
		if err := os.MkdirAll(models, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := record.New("haedus", "1.4.0", "user", map[string]string{"models": models})
	_ = r.Add(record.Entry{Path: mine, Kind: record.KindFile, Owner: record.OwnerInstaller, Size: 1100, Location: "models"})
	_ = r.Add(record.Entry{Path: theirs, Kind: record.KindFile, Owner: record.OwnerAdopted, Size: 3100, Location: "models"})
	rec := filepath.Join(root, "record.json")
	if err := r.Save(rec); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := do(t, "plan", rec)
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if !strings.Contains(out, "remove") || !strings.Contains(out, "protected") || !strings.Contains(out, "nothing was removed") {
		t.Fatalf("unexpected plan output:\n%s", out)
	}
	for _, p := range []string{mine, theirs} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("plan removed %s", p)
		}
	}
	_, out, _ = do(t, "plan", "--keep-path", mine, rec)
	if !strings.Contains(out, "keep") {
		t.Fatalf("--keep-path was ignored:\n%s", out)
	}
}
