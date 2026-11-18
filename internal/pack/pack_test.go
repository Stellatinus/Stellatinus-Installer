package pack

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPackThenReadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	engine := bytes.Repeat([]byte("MZ-engine-"), 1000)
	write(t, filepath.Join(dir, "engine"), engine)
	manifest := []byte(`{"schema_version": 1}`)
	out := filepath.Join(dir, "setup")
	if err := Pack(filepath.Join(dir, "engine"), manifest, out); err != nil {
		t.Fatal(err)
	}
	got, err := Read(out)
	if err != nil || !bytes.Equal(got, manifest) {
		t.Fatalf("Read = %q, %v", got, err)
	}
	all, _ := os.ReadFile(out)
	if !bytes.HasPrefix(all, engine) {
		t.Fatal("the engine bytes were changed: a signature on the engine part would not survive")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(out); fi.Mode()&0o111 == 0 {
			t.Error("the packed file is not executable")
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".pack-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestAPlainEngineIsNotPacked(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"empty":     {},
		"tiny":      []byte("x"),
		"plain":     bytes.Repeat([]byte("plain engine "), 500),
		"magic-ish": []byte(strings.Repeat("a", 100) + "STELLATINUSPACK0"),
	} {
		p := filepath.Join(dir, name)
		write(t, p, data)
		if _, err := Read(p); !errors.Is(err, ErrNotPacked) {
			t.Errorf("%s: %v, want ErrNotPacked", name, err)
		}
	}
	if _, err := Read(filepath.Join(dir, "missing")); err == nil || errors.Is(err, ErrNotPacked) {
		t.Errorf("a missing file must be an error of its own: %v", err)
	}
}

func TestATamperedOrCutFileIsCorruptNotPlain(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "engine"), bytes.Repeat([]byte("e"), 500))
	manifest := []byte(`{"app": "original"}`)
	good := filepath.Join(dir, "good")
	if err := Pack(filepath.Join(dir, "engine"), manifest, good); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(good)

	// A changed byte inside the manifest.
	changed := append([]byte{}, data...)
	i := bytes.Index(changed, []byte("original"))
	changed[i] = 'X'
	write(t, filepath.Join(dir, "changed"), changed)
	if _, err := Read(filepath.Join(dir, "changed")); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a changed manifest: %v, want ErrCorrupt", err)
	}

	// A length that does not fit.
	badLen := append([]byte{}, data...)
	badLen[len(badLen)-trailerLen+7] = 0xff
	write(t, filepath.Join(dir, "badlen"), badLen)
	if _, err := Read(filepath.Join(dir, "badlen")); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a bad length: %v, want ErrCorrupt", err)
	}

	// A zero length.
	zero := append([]byte{}, data...)
	for k := 0; k < 8; k++ {
		zero[len(zero)-trailerLen+k] = 0
	}
	write(t, filepath.Join(dir, "zero"), zero)
	if _, err := Read(filepath.Join(dir, "zero")); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a zero length: %v, want ErrCorrupt", err)
	}
}

func TestPackingAPackedFileReplacesTheManifest(t *testing.T) {
	dir := t.TempDir()
	engine := bytes.Repeat([]byte("engine"), 300)
	write(t, filepath.Join(dir, "engine"), engine)
	first := filepath.Join(dir, "first")
	if err := Pack(filepath.Join(dir, "engine"), []byte("one"), first); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "second")
	if err := Pack(first, []byte("two"), second); err != nil {
		t.Fatal(err)
	}
	if m, err := Read(second); err != nil || string(m) != "two" {
		t.Fatalf("Read = %q, %v", m, err)
	}
	a, _ := os.ReadFile(second)
	if !bytes.HasPrefix(a, engine) || len(a) != len(engine)+len("two")+trailerLen {
		t.Fatalf("the old manifest was kept inside the engine part (%d bytes)", len(a))
	}
}

func TestEngineReturnsOnlyTheEnginePart(t *testing.T) {
	dir := t.TempDir()
	engine := bytes.Repeat([]byte("E"), 400)
	write(t, filepath.Join(dir, "engine"), engine)
	if err := Pack(filepath.Join(dir, "engine"), []byte("m"), filepath.Join(dir, "packed")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"engine", "packed"} {
		r, err := Engine(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		_ = r.Close()
		if !bytes.Equal(b, engine) {
			t.Errorf("Engine(%s) returned %d bytes, want the %d engine bytes", name, len(b), len(engine))
		}
	}
}

func TestPackRefusesBadInput(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "engine"), []byte("e"))
	if err := Pack(filepath.Join(dir, "engine"), nil, filepath.Join(dir, "o")); err == nil {
		t.Error("an empty manifest was accepted")
	}
	if err := Pack(filepath.Join(dir, "engine"), bytes.Repeat([]byte("x"), MaxManifest+1), filepath.Join(dir, "o")); err == nil {
		t.Error("an oversized manifest was accepted")
	}
	if err := Pack(filepath.Join(dir, "engine"), []byte("m"), filepath.Join(dir, "engine")); err == nil {
		t.Error("packing over the engine file itself was accepted")
	}
	if err := Pack(filepath.Join(dir, "missing"), []byte("m"), filepath.Join(dir, "o")); err == nil {
		t.Error("a missing engine was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "o")); err == nil {
		t.Error("a failed pack left an output file")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".pack-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
