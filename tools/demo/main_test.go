package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stellatinus/Stellatinus-Installer/internal/install"
	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
	"github.com/Stellatinus/Stellatinus-Installer/internal/shortcut"
)

const demoManifest = "../../examples/stellatinus-demo.json"

func TestGeneratedManifestMatchesTheFiles(t *testing.T) {
	out := t.TempDir()
	if err := generate(demoManifest, out, 1<<20); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Components {
		name := path.Base(strings.SplitN(c.URL, "?", 2)[0])
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("component %s: the file %s was not written: %v", c.ID, name, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != c.SHA256 || int64(len(data)) != c.Size {
			t.Errorf("component %s: the manifest says %s (%d bytes), the file is %x (%d bytes)", c.ID, c.SHA256, c.Size, sum, len(data))
		}
		if !strings.HasPrefix(c.URL, "https://github.com/Stellatinus/Stellatinus-Installer/releases/download/test-builds/") {
			t.Errorf("component %s: %s is not an asset of the test-builds release", c.ID, c.URL)
		}
	}
}

func TestPayloadIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		if err := generate(demoManifest, d, 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	ma, _ := os.ReadFile(filepath.Join(a, "manifest.json"))
	mb, _ := os.ReadFile(filepath.Join(b, "manifest.json"))
	if !bytes.Equal(ma, mb) {
		t.Fatal("two runs gave different manifests: the hashes are not stable")
	}
}

func TestTheZipHoldsTheProgramTheManifestLaunches(t *testing.T) {
	m, err := manifest.Load(demoManifest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := demoZip()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, f := range zr.File {
		have[f.Name] = true
	}
	for goos, rel := range m.Launch.Path {
		if !have[rel] {
			t.Errorf("the manifest launches %s on %s, but the zip does not hold it (%v)", rel, goos, have)
		}
	}
}

func TestAComponentTheGeneratorDoesNotKnowIsRefused(t *testing.T) {
	raw, err := os.ReadFile(demoManifest)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "m.json")
	doc := strings.ReplaceAll(string(raw), "sample_model", "other_thing")
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(p, t.TempDir(), 1<<20); err == nil || !strings.Contains(err.Error(), "other_thing") {
		t.Fatalf("got %v", err)
	}
}

// The demo, installed from its own generated payload over a local HTTPS server,
// with a shortcut, then removed: what a person does with the test installer.
func TestTheDemoInstallsAndIsRemovedCleanly(t *testing.T) {
	out := t.TempDir()
	if err := generate(demoManifest, out, 1<<20); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.FileServer(http.Dir(out)))
	t.Cleanup(srv.Close)
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.ReplaceAll(string(raw), "https://github.com/Stellatinus/Stellatinus-Installer/releases/download/test-builds", srv.URL)
	m, err := manifest.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}

	root, home := t.TempDir(), t.TempDir()
	paths := map[string]string{}
	for _, l := range m.Locations {
		paths[l.ID] = filepath.Join(root, l.ID)
	}
	// The models folder already holds the user's own file, which must survive.
	if err := os.MkdirAll(paths["models"], 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(paths["models"], "my-model.bin")
	if err := os.WriteFile(mine, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := install.Run(context.Background(), m, install.Options{
		Client: srv.Client(), Paths: paths, RecordPath: filepath.Join(root, "state", "record.json"),
		GOOS: "linux", GOARCH: "amd64", Env: func(k string) string { return map[string]string{"HOME": home}[k] },
		Shortcuts: []shortcut.Place{shortcut.StartMenu},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shortcuts) != 1 || len(res.ShortcutNotes) != 0 {
		t.Fatalf("shortcuts %v, notes %v", res.Shortcuts, res.ShortcutNotes)
	}
	for _, p := range []string{
		filepath.Join(paths["install"], "bin", "stellatinus-demo"),
		filepath.Join(paths["install"], "config.json"),
		filepath.Join(paths["models"], "demo-model.bin"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("not installed: %v", err)
		}
	}
	if rep := res.Record.Apply(res.Record.Plan(record.Choices{})); len(rep.Failed) != 0 {
		t.Fatalf("removal: %+v", rep.Failed)
	}
	if got, _ := os.ReadFile(mine); string(got) != "mine" {
		t.Fatal("the user's file in the adopted folder was touched")
	}
	if _, err := os.Stat(filepath.Join(paths["install"])); err == nil {
		t.Error("the program folder is left behind")
	}
	if _, err := os.Lstat(res.Shortcuts[0]); err == nil {
		t.Error("the shortcut is left behind")
	}
	if _, err := os.Stat(filepath.Join(paths["models"], "demo-model.bin")); err == nil {
		t.Error("the sample model is left behind")
	}
}
