// Command demo makes the payload of the test installer: the files the demo
// manifest downloads (a small zip and a larger generated file) and a copy of the
// manifest with their real sha256 and size filled in, so CI can publish both
// and pack the manifest into the installers. The files are fake and say so;
// nothing in them runs. It does not publish anything and does not touch the
// installer engine.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
)

func main() {
	manifestPath := flag.String("manifest", "examples/stellatinus-demo.json", "the demo manifest, with placeholder hashes")
	out := flag.String("out", "", "the folder to write the payload and manifest.json into (required)")
	binDir := flag.String("bin-dir", "", "the folder with the built demo programs, stellatinus-demo-<os>-<arch>[.exe] (default: text placeholders)")
	modelMiB := flag.Int("model-mib", 32, "size of the generated sample model, in MiB")
	flag.Parse()
	if *out == "" || *modelMiB < 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := generate(*manifestPath, *out, *binDir, int64(*modelMiB)<<20); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// payload returns the bytes of a component of the demo manifest. A component
// the generator does not know is an error, so the manifest and the generator
// cannot drift apart without a test failing. The program of each system comes
// from binDir (built by CI from tools/demoapp as stellatinus-demo-<os>-<arch>,
// with .exe on Windows); without binDir a text placeholder stands in, which is
// enough for tests but is not a program.
func payload(m *manifest.Manifest, c manifest.Component, binDir string, modelSize int64) ([]byte, error) {
	if c.ID == "sample_model" {
		return demoModel(modelSize), nil
	}
	if len(c.Platforms) != 1 || !strings.HasPrefix(c.ID, "app_") {
		return nil, fmt.Errorf("the demo manifest has a component %q that this generator does not know how to make", c.ID)
	}
	goos, goarch, _ := strings.Cut(c.Platforms[0], "/")
	rel, ok := m.Launch.Path[goos]
	if !ok || goarch == "" {
		return nil, fmt.Errorf("component %q: the manifest launches nothing on %s", c.ID, c.Platforms[0])
	}
	prog := []byte("Placeholder: Stellatinus Demo is built from tools/demoapp.\n")
	if binDir != "" {
		name := "stellatinus-demo-" + goos + "-" + goarch
		if goos == "windows" {
			name += ".exe"
		}
		b, err := os.ReadFile(filepath.Join(binDir, name))
		if err != nil {
			return nil, fmt.Errorf("component %q: %w", c.ID, err)
		}
		prog = b
	}
	return demoZip(rel, prog)
}

// demoZip holds the program at rel (executable) and a README.
func demoZip(rel string, program []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct {
		name string
		mode os.FileMode
		data []byte
	}{
		{"README.txt", 0o644, []byte("Stellatinus Demo\n\nA small app made to try the installer. It prints a message and does nothing else.\n")},
		{rel, 0o755, program},
	}
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		h.SetMode(f.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// demoModel is size bytes of noise from a fixed seed: it does not compress and
// it is the same on every run, so the hash is stable.
func demoModel(size int64) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, size)
	for i := 0; i+8 <= len(b); i += 8 {
		v := r.Uint64()
		for j := 0; j < 8; j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	return b
}

func generate(manifestPath, out, binDir string, modelSize int64) error {
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	comps, _ := doc["components"].([]any)
	if len(comps) != len(m.Components) {
		return fmt.Errorf("the manifest lists %d components and %d were read", len(comps), len(m.Components))
	}
	for i, c := range m.Components {
		data, err := payload(m, c, binDir, modelSize)
		if err != nil {
			return err
		}
		u, err := url.Parse(c.URL)
		if err != nil {
			return err
		}
		name := path.Base(u.Path)
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		entry, _ := comps[i].(map[string]any)
		entry["sha256"] = hex.EncodeToString(sum[:])
		entry["size"] = len(data)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := manifest.Parse(bytes.NewReader(b)); err != nil {
		return fmt.Errorf("the filled manifest is not valid: %w", err)
	}
	return os.WriteFile(filepath.Join(out, "manifest.json"), b, 0o644)
}
