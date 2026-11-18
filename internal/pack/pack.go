// Package pack makes a single-file installer: the engine's executable with an
// app's manifest appended to it, so one file is all a person has to download
// and the engine and the manifest can never be a mismatched pair. The manifest
// sits after the executable, followed by a trailer with its length, its sha256
// and a magic string; a signature applied to the file afterwards (Authenticode)
// covers it. It does not run anything, parse the manifest or install anything.
package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Magic ends every packed file.
const Magic = "STELLATINUSPACK1"

const (
	// MaxManifest bounds the manifest, as manifest.Parse does.
	MaxManifest = 1 << 20
	trailerLen  = 8 + sha256.Size + len(Magic)
)

// Errors from Read.
var (
	// ErrNotPacked means the file has no manifest appended: it is the plain
	// engine.
	ErrNotPacked = errors.New("the file has no manifest appended")
	// ErrCorrupt means the file claims to carry a manifest but the trailer or
	// the checksum does not hold: the file was cut short or changed.
	ErrCorrupt = errors.New("the appended manifest is damaged")
)

// trailer builds the bytes that follow the manifest.
func trailer(manifest []byte) []byte {
	var t bytes.Buffer
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(manifest)))
	t.Write(n[:])
	sum := sha256.Sum256(manifest)
	t.Write(sum[:])
	t.WriteString(Magic)
	return t.Bytes()
}

// Write copies the engine, then the manifest and its trailer, to w.
func Write(w io.Writer, engine io.Reader, manifest []byte) error {
	if len(manifest) == 0 || len(manifest) > MaxManifest {
		return fmt.Errorf("the manifest must be 1 to %d bytes, got %d", MaxManifest, len(manifest))
	}
	if _, err := io.Copy(w, engine); err != nil {
		return err
	}
	if _, err := w.Write(manifest); err != nil {
		return err
	}
	_, err := w.Write(trailer(manifest))
	return err
}

// Read returns the manifest appended to the file at path. A file with no
// magic at its end gives ErrNotPacked; one with the magic but a bad length or
// checksum gives ErrCorrupt.
func Read(path string) ([]byte, error) {
	m, _, err := locate(path)
	return m, err
}

// locate returns the manifest and the size of the engine part.
func locate(path string) (manifest []byte, engineSize int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := info.Size()
	if size < int64(trailerLen) {
		return nil, 0, ErrNotPacked
	}
	tail := make([]byte, trailerLen)
	if _, err := f.ReadAt(tail, size-int64(trailerLen)); err != nil {
		return nil, 0, err
	}
	if string(tail[len(tail)-len(Magic):]) != Magic {
		return nil, 0, ErrNotPacked
	}
	n := binary.LittleEndian.Uint64(tail[:8])
	if n == 0 || n > MaxManifest || int64(n) > size-int64(trailerLen) {
		return nil, 0, fmt.Errorf("%w: the length %d does not fit the file", ErrCorrupt, n)
	}
	manifest = make([]byte, n)
	start := size - int64(trailerLen) - int64(n)
	if _, err := f.ReadAt(manifest, start); err != nil {
		return nil, 0, err
	}
	want := tail[8 : 8+sha256.Size]
	got := sha256.Sum256(manifest)
	if !bytes.Equal(got[:], want) {
		return nil, 0, fmt.Errorf("%w: the checksum does not match", ErrCorrupt)
	}
	return manifest, start, nil
}

// Engine opens the engine part of a file: the whole file if it is not packed,
// or the part before the manifest if it is. This is how a packed installer can
// be packed again with another manifest.
func Engine(path string) (io.ReadCloser, error) {
	_, engineSize, err := locate(path)
	if errors.Is(err, ErrNotPacked) {
		return os.Open(path)
	}
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(f, engineSize), f}, nil
}

// Pack writes engine plus manifest to out, through a temporary file that
// replaces out only when complete, with execute permission. If the engine is
// itself packed, only its engine part is used.
func Pack(enginePath string, manifest []byte, out string) error {
	eng, err := Engine(enginePath)
	if err != nil {
		return err
	}
	defer func() { _ = eng.Close() }()
	if abs1, e1 := filepath.Abs(enginePath); e1 == nil {
		if abs2, e2 := filepath.Abs(out); e2 == nil && abs1 == abs2 {
			return errors.New("the output must not be the engine file itself")
		}
	}
	dir := filepath.Dir(out)
	tmp, err := os.CreateTemp(dir, ".pack-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := Write(tmp, eng, manifest); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o755); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, out); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
