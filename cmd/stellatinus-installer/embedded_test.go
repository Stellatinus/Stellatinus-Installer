package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stellatinus/Stellatinus-Installer/internal/pack"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

// packedRig makes a stand-in engine file and packs the rig's manifest into it.
func packedRig(t *testing.T, r *rig) string {
	t.Helper()
	dir := t.TempDir()
	engine := filepath.Join(dir, "engine")
	if err := os.WriteFile(engine, bytes.Repeat([]byte("E"), 600), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "setup")
	var o, e bytes.Buffer
	code := runWith(context.Background(), deps{}, []string{"pack", "--engine", engine, "--manifest", r.manifest, "--out", out}, &o, &e)
	if code != exitOK || !strings.Contains(o.String(), "Demo 1.0.0") {
		t.Fatalf("pack: %d\n%s%s", code, o.String(), e.String())
	}
	return out
}

func (r *rig) runPacked(exe, stdin string, args ...string) (int, string, string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code := runWith(context.Background(), deps{client: r.srv.Client(), exe: exe, stdin: strings.NewReader(stdin)}, args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestPackedInstallerInstallsWithoutAManifestFile(t *testing.T) {
	r := goodRig(t)
	exe := packedRig(t, r)
	code, out, errOut := r.runPacked(exe, "", "install", "--set", "install="+r.app, "--set", "models="+r.models)
	if code != exitOK || !strings.Contains(out, "Installed Demo 1.0.0") {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(r.app, "bin", "app.exe")); string(b) != "binary" {
		t.Fatal("not installed")
	}
	// check also works from the embedded manifest.
	if code, out, _ := r.runPacked(exe, "", "check", "--set", "install="+r.root+"/other", "--set", "models="+r.root+"/m2"); code != exitOK {
		t.Fatalf("check: %d\n%s", code, out)
	}
}

func TestPackedInstallerAsksWhenStartedWithNothing(t *testing.T) {
	r := goodRig(t)
	exe := packedRig(t, r)

	for _, answer := range []string{"", "n\n", "\n", "maybe\n"} {
		code, out, _ := r.runPacked(exe, answer)
		if code != exitOK || !strings.Contains(out, "Nothing was installed") {
			t.Fatalf("answer %q: %d\n%s", answer, code, out)
		}
	}
	if _, err := record.DefaultPath("demo", "user"); err == nil {
		if p, _ := record.DefaultPath("demo", "user"); exists(p) {
			t.Fatal("something was installed without a yes")
		}
	}

	code, out, errOut := r.runPacked(exe, "y\n")
	if code != exitOK || !strings.Contains(out, "Installed Demo 1.0.0") {
		t.Fatalf("yes: %d\n%s%s", code, out, errOut)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPlainEngineStillNeedsAManifest(t *testing.T) {
	r := goodRig(t)
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, bytes.Repeat([]byte("P"), 600), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"install"}, {"check"}} {
		code, _, errOut := r.runPacked(plain, "y\n", args...)
		if code != exitUsage || !strings.Contains(errOut, "Usage:") {
			t.Errorf("%v: %d %q, want usage", args, code, errOut)
		}
	}
}

func TestDamagedPackedInstallerIsRefused(t *testing.T) {
	r := goodRig(t)
	exe := packedRig(t, r)
	data, _ := os.ReadFile(exe)
	i := bytes.Index(data, []byte("schema_version"))
	data[i] = 'X'
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, data, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"install"}} {
		code, _, errOut := r.runPacked(bad, "y\n", args...)
		if code != exitProblem || !strings.Contains(errOut, "damaged") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
	if _, err := os.Stat(r.app); err == nil {
		t.Fatal("a damaged installer installed something")
	}
}

func TestAFileNamedOnTheCommandLineWinsOverTheEmbeddedOne(t *testing.T) {
	r := goodRig(t)
	exe := packedRig(t, r)
	code, out, _ := r.runPacked(exe, "", "validate", r.manifest)
	if code != exitOK || !strings.Contains(out, "valid") {
		t.Fatalf("%d %s", code, out)
	}
	if code, _, _ := r.runPacked(exe, "", "install", "a.json", "b.json"); code != exitUsage {
		t.Fatalf("two manifests: %d", code)
	}
}

func TestPackRefusesABrokenManifestAndBadUsage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"schema_version": 1}`), 0o600)
	var o, e bytes.Buffer
	out := filepath.Join(dir, "out")
	if code := runWith(context.Background(), deps{}, []string{"pack", "--manifest", bad, "--out", out}, &o, &e); code != exitProblem {
		t.Errorf("a broken manifest: %d", code)
	}
	if exists(out) {
		t.Error("an installer was written for a broken manifest")
	}
	if code := runWith(context.Background(), deps{}, []string{"pack", "--out", out}, &o, &e); code != exitUsage {
		t.Errorf("no manifest: %d", code)
	}
}

// registerRigInstalled installs the demo and returns the models folder.
func registerRigInstalled(t *testing.T) *rig {
	t.Helper()
	r := goodRig(t)
	if code, out, e := r.run(r.args()...); code != exitOK {
		t.Fatalf("install: %d %s%s", code, out, e)
	}
	return r
}

func TestRegisterMakesLaterFilesPartOfTheUninstall(t *testing.T) {
	r := registerRigInstalled(t)
	later := filepath.Join(r.models, "later.bin")
	_ = os.WriteFile(later, []byte("x"), 0o644)
	outside := filepath.Join(r.root, "outside.bin")
	_ = os.WriteFile(outside, []byte("x"), 0o644)

	if code, _, e := r.run("register", "--location", "models", "demo", outside); code != exitProblem {
		t.Fatalf("a path outside the location: %d %s", code, e)
	}
	if code, _, _ := r.run("register", "demo", later); code != exitUsage {
		t.Fatalf("no --location: %d", code)
	}
	code, out, e := r.run("register", "--location", "models", "--origin", "catalog", "demo", later)
	if code != exitOK || !strings.Contains(out, "Registered 1 entry") {
		t.Fatalf("register: %d %s%s", code, out, e)
	}
	if code, out, _ := r.run("uninstall", "--yes", "demo"); code != exitOK {
		t.Fatalf("uninstall: %d %s", code, out)
	}
	if exists(later) {
		t.Fatal("the registered file was not removed")
	}
	if !exists(outside) {
		t.Fatal("a file outside the installation was removed")
	}
}

func TestUnregisterKeepsTheFile(t *testing.T) {
	r := registerRigInstalled(t)
	later := filepath.Join(r.models, "later.bin")
	_ = os.WriteFile(later, []byte("x"), 0o644)
	if code, _, e := r.run("register", "--location", "models", "demo", later); code != exitOK {
		t.Fatal(e)
	}
	code, out, _ := r.run("unregister", "demo", later)
	if code != exitOK || !strings.Contains(out, "Unregistered 1 entry") {
		t.Fatalf("%d %s", code, out)
	}
	// The folder the installer made now holds a file it does not own, so it is
	// left, and the run says so (exit 5) instead of deleting the file.
	if code, out, e := r.run("uninstall", "--yes", "demo"); code != exitLeftovers || !strings.Contains(e, "is not empty") {
		t.Fatalf("uninstall: %d %s%s", code, out, e)
	}
	if !exists(later) {
		t.Fatal("a file the app took back was removed with the installation")
	}
	// An unknown app has no record.
	if code, _, _ := r.run("register", "--location", "models", "nothing", later); code != exitProblem {
		t.Fatalf("unknown app: %d", code)
	}
	_ = pack.Magic
}
