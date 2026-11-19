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
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
)

func main() {
	manifestPath := flag.String("manifest", "examples/stellatinus-demo.json", "the demo manifest, with placeholder hashes")
	out := flag.String("out", "", "the folder to write the payload and manifest.json into (required)")
	modelMiB := flag.Int("model-mib", 32, "size of the generated sample model, in MiB")
	flag.Parse()
	if *out == "" || *modelMiB < 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := generate(*manifestPath, *out, int64(*modelMiB)<<20); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// payload returns the bytes of each component of the demo manifest, by id. A
// component the generator does not know is an error, so the manifest and the
// generator cannot drift apart without a test failing.
func payload(id string, modelSize int64) ([]byte, error) {
	switch id {
	case "app":
		return demoZip()
	case "sample_model":
		return demoModel(modelSize), nil
	}
	return nil, fmt.Errorf("the demo manifest has a component %q that this generator does not know how to make", id)
}

func demoZip() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct {
		name string
		mode os.FileMode
		data string
	}{
		{"README.txt", 0o644, "Stellatinus Demo\n\nThis is a fake app made to try the installer. Nothing in it runs.\n"},
		{"bin/stellatinus-demo", 0o755, "#!/bin/sh\necho 'Stellatinus Demo: a fake program, it does nothing.'\n"},
		{"bin/StellatinusDemo.exe", 0o644, "Not a real program: Stellatinus Demo is a fake app.\n"},
	}
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		h.SetMode(f.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, f.data); err != nil {
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

func generate(manifestPath, out string, modelSize int64) error {
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
		data, err := payload(c.ID, modelSize)
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
