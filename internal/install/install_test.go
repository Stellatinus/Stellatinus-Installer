package install

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func tlsServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func serveBytes(data []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x", time.Time{}, bytes.NewReader(data))
	})
}

type zent struct {
	name   string
	data   []byte
	mode   os.FileMode
	method uint16
}

func makeZip(t *testing.T, entries ...zent) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.method != 0 {
			h.Method = e.method
		}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// comp describes a component for the test manifests.
type comp struct {
	id, kind, location, name, dest, url string
	data                                []byte
}

func build(t *testing.T, comps ...comp) *manifest.Manifest {
	t.Helper()
	var cs []map[string]any
	for _, c := range comps {
		m := map[string]any{
			"id": c.id, "label": "the " + c.id, "url": c.url, "sha256": sum(c.data),
			"size": len(c.data), "location": c.location,
		}
		if c.kind != "" {
			m["kind"] = c.kind
		}
		if c.name != "" {
			m["name"] = c.name
		}
		if c.dest != "" {
			m["dest"] = c.dest
		}
		cs = append(cs, m)
	}
	doc := map[string]any{
		"schema_version": 1,
		"app":            map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "accent": "#2f6df6"},
		"locations": []any{
			map[string]any{"id": "install", "label": "Program", "kind": "install", "default": map[string]any{"linux": "/x"}},
			map[string]any{"id": "models", "label": "Models", "kind": "models", "adopt": true, "default": map[string]any{"linux": "/y"}},
		},
		"components": cs,
	}
	b, _ := json.Marshal(doc)
	m, err := manifest.Parse(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type env struct {
	root  string
	paths map[string]string
	rec   string
}

func newEnv(t *testing.T) env {
	root := t.TempDir()
	return env{root: root, rec: filepath.Join(root, "state", "record.json"), paths: map[string]string{
		"install": filepath.Join(root, "install"), "models": filepath.Join(root, "models"),
	}}
}

func (e env) opts(srv *httptest.Server) Options {
	return Options{Client: srv.Client(), Paths: e.paths, RecordPath: e.rec, IdleTimeout: 5 * time.Second,
		Attempts: 3, Backoff: func(int) time.Duration { return 0 }}
}

// snapshot lists everything under dir, so a test can prove nothing changed.
func snapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		kind := "f "
		if d.IsDir() {
			kind = "d "
		}
		out = append(out, kind+rel)
		return nil
	})
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

func mustRolledBack(t *testing.T, err error) {
	t.Helper()
	var ie *Error
	if !errors.As(err, &ie) {
		t.Fatalf("want an *install.Error, got %v", err)
	}
	if !ie.RolledBack {
		t.Fatalf("not rolled back: %+v", ie.Failures)
	}
}

func TestInstallsAFileAndAZipThenRemovesEverything(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.root)
	zipData := makeZip(t,
		zent{name: "bin/app.exe", data: []byte("binary"), mode: 0o755},
		zent{name: "readme.txt", data: []byte("hello")},
		zent{name: "data/sub/x.txt", data: []byte("x")},
		zent{name: "empty/", method: zip.Store},
	)
	model := bytes.Repeat([]byte("model"), 2000)
	mux := http.NewServeMux()
	mux.Handle("/core.zip", serveBytes(zipData))
	mux.Handle("/dl/voice.bin", serveBytes(model))
	srv := tlsServer(t, mux)
	m := build(t,
		comp{id: "core", kind: "zip", location: "install", url: srv.URL + "/core.zip", data: zipData},
		comp{id: "voice", location: "models", url: srv.URL + "/dl/voice.bin", data: model},
	)

	var events []Event
	var logs []string
	opt := e.opts(srv)
	opt.Events = func(ev Event) { events = append(events, ev) }
	opt.Log = func(s string) { logs = append(logs, s) }
	res, err := Run(context.Background(), m, opt)
	if err != nil {
		t.Fatal(err)
	}

	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	if read(filepath.Join(e.paths["install"], "bin", "app.exe")) != "binary" || read(filepath.Join(e.paths["install"], "data", "sub", "x.txt")) != "x" {
		t.Fatal("zip content was not unpacked")
	}
	if _, err := os.Stat(filepath.Join(e.paths["install"], "empty")); err != nil {
		t.Fatalf("empty folder entry: %v", err)
	}
	if got := read(filepath.Join(e.paths["models"], "voice.bin")); got != string(model) {
		t.Fatal("model file differs")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(e.paths["install"], "bin", "app.exe")); fi.Mode()&0o111 == 0 {
			t.Error("the executable bit was lost")
		}
	}
	// No partial or temporary file is left.
	for _, s := range snapshot(t, e.root) {
		if strings.Contains(s, ".part") || strings.Contains(s, ".stellatinus-tmp") {
			t.Errorf("left behind: %s", s)
		}
	}

	// The record on disk matches and says who owns what.
	loaded, err := record.Load(e.rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != len(res.Record.Entries) {
		t.Fatalf("record on disk has %d entries, result has %d", len(loaded.Entries), len(res.Record.Entries))
	}
	var sawFile bool
	for _, en := range loaded.Entries {
		if en.Owner != record.OwnerInstaller {
			t.Errorf("%s: owner %s, want installer (everything was created here)", en.Path, en.Owner)
		}
		if filepath.Base(en.Path) == "voice.bin" {
			sawFile = en.Origin == "voice" && en.Size == int64(len(model)) && en.Location == "models"
		}
	}
	if !sawFile {
		t.Error("voice.bin is not recorded with its origin, size and location")
	}

	// Progress: the label is the plain sentence, each download ends complete.
	last := map[string]Event{}
	for _, ev := range events {
		if ev.Phase == PhaseDownload {
			if prev, ok := last[ev.Component]; ok && ev.Done < prev.Done {
				t.Errorf("progress went backwards for %s", ev.Component)
			}
			last[ev.Component] = ev
			if ev.Label != "the "+ev.Component {
				t.Errorf("label %q", ev.Label)
			}
		}
	}
	for _, id := range []string{"core", "voice"} {
		if last[id].Done != last[id].Total || last[id].Total == 0 {
			t.Errorf("%s: last download event %d of %d", id, last[id].Done, last[id].Total)
		}
	}
	if len(logs) == 0 {
		t.Error("nothing was logged")
	}

	// Removing by the record leaves the machine as it was.
	rep := loaded.Apply(loaded.Plan(record.Choices{}))
	if len(rep.Failed) != 0 {
		t.Fatalf("removal failures: %+v", rep.Failed)
	}
	if after := snapshot(t, e.root); !equal(after, append(before, "d state", "f state/record.json")) && !equal(after, append(before, "d state", "f state"+string(filepath.Separator)+"record.json")) {
		t.Fatalf("not clean after removal:\nbefore %v\nafter  %v", before, after)
	}
}

func TestAWrongHashRollsBackEverything(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.root)
	good := []byte(strings.Repeat("a", 500))
	bad := []byte(strings.Repeat("b", 500)) // same length, other content
	mux := http.NewServeMux()
	mux.Handle("/a", serveBytes(good))
	mux.Handle("/b", serveBytes(bad))
	srv := tlsServer(t, mux)
	m := build(t,
		comp{id: "a", location: "install", name: "a.bin", url: srv.URL + "/a", data: good},
		comp{id: "b", location: "install", name: "b.bin", url: srv.URL + "/b", data: good}, // manifest hash is of "good"
	)
	_, err := Run(context.Background(), m, e.opts(srv))
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("want ErrHashMismatch, got %v", err)
	}
	mustRolledBack(t, err)
	if after := snapshot(t, e.root); !equal(after, before) {
		t.Fatalf("not rolled back:\nbefore %v\nafter  %v", before, after)
	}
}

func TestAnAdoptedFolderSurvivesAFailure(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.paths["models"], 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(e.paths["models"], "whisper-large-v3")
	if err := os.WriteFile(mine, []byte("the user's model"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := []byte(strings.Repeat("m", 400))
	other := []byte(strings.Repeat("o", 400))
	mux := http.NewServeMux()
	mux.Handle("/model", serveBytes(model))
	mux.Handle("/other", serveBytes([]byte(strings.Repeat("x", 400)))) // wrong content
	srv := tlsServer(t, mux)
	m := build(t,
		comp{id: "model", location: "models", name: "ggml.bin", url: srv.URL + "/model", data: model},
		comp{id: "other", location: "install", name: "o.bin", url: srv.URL + "/other", data: other},
	)
	_, err := Run(context.Background(), m, e.opts(srv))
	mustRolledBack(t, err)
	if b, _ := os.ReadFile(mine); string(b) != "the user's model" {
		t.Fatal("the user's file was touched")
	}
	if got := snapshot(t, e.paths["models"]); !equal(got, []string{"d .", "f whisper-large-v3"}) {
		t.Fatalf("models folder after rollback: %v", got)
	}
	if _, err := os.Stat(e.paths["install"]); err == nil {
		t.Fatal("the install folder this run created is still there")
	}
}

func TestResumesADroppedDownload(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("0123456789"), 5000) // 50 000 bytes
	var mu sync.Mutex
	var ranges []string
	calls := 0
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data[:20000])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // drop the connection mid-body
		}
		http.ServeContent(w, r, "x", time.Time{}, bytes.NewReader(data))
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	if _, err := Run(context.Background(), m, e.opts(srv)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.paths["install"], "f.bin")); !bytes.Equal(b, data) {
		t.Fatal("the resumed file differs")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ranges) != 2 || ranges[0] != "" || !strings.HasPrefix(ranges[1], "bytes=") || !strings.HasSuffix(ranges[1], "-") {
		t.Fatalf("expected a plain request then a range request, got %q", ranges)
	}
}

func TestAServerThatIgnoresTheRangeStartsOver(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("abcdefghij"), 3000)
	calls := 0
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if calls == 1 {
			_, _ = w.Write(data[:9000])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write(data) // 200 with the whole body, whatever was asked
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	if _, err := Run(context.Background(), m, e.opts(srv)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.paths["install"], "f.bin")); !bytes.Equal(b, data) {
		t.Fatal("file differs after the server ignored the range")
	}
}

func TestARefusedResumeStartsOver(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("z"), 8000)
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		_, _ = w.Write(data)
	}))
	// A leftover partial file from a crashed run.
	if err := os.MkdirAll(e.paths["install"], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.paths["install"], ".stellatinus-f.part"), data[:100], 0o644); err != nil {
		t.Fatal(err)
	}
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	if _, err := Run(context.Background(), m, e.opts(srv)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.paths["install"], "f.bin")); !bytes.Equal(b, data) {
		t.Fatal("file differs after a refused resume")
	}
}

func TestACompletePartialFileIsNotDownloadedAgain(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("q"), 3000)
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the server was called although the partial file was complete")
	}))
	if err := os.MkdirAll(e.paths["install"], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.paths["install"], ".stellatinus-f.part"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	if _, err := Run(context.Background(), m, e.opts(srv)); err != nil {
		t.Fatal(err)
	}
}

func TestACompletePartialFileWithTheWrongContentIsRefused(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("q"), 3000)
	srv := tlsServer(t, serveBytes(data))
	if err := os.MkdirAll(e.paths["install"], 0o755); err != nil {
		t.Fatal(err)
	}
	// Same length, tampered content: the hash must catch it.
	if err := os.WriteFile(filepath.Join(e.paths["install"], ".stellatinus-f.part"), bytes.Repeat([]byte("Q"), 3000), 0o644); err != nil {
		t.Fatal(err)
	}
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	_, err := Run(context.Background(), m, e.opts(srv))
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("a tampered partial file must fail the hash check, got %v", err)
	}
}

func TestAStalledServerIsGivenUpOn(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("s"), 5000)
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data[:1000])
		w.(http.Flusher).Flush()
		<-r.Context().Done() // say nothing more
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	opt := e.opts(srv)
	opt.IdleTimeout, opt.Attempts = 250*time.Millisecond, 1
	start := time.Now()
	_, err := Run(context.Background(), m, opt)
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("want a stall error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the stall took far too long to detect")
	}
	mustRolledBack(t, err)
}

func TestARedirectToHTTPIsRefused(t *testing.T) {
	e := newEnv(t)
	data := []byte("redirected")
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:9/evil", http.StatusFound)
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	_, err := Run(context.Background(), m, e.opts(srv))
	if err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("want a refusal of the http redirect, got %v", err)
	}
	mustRolledBack(t, err)
}

func TestASizeThatDiffersFromTheManifestIsRefused(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("d"), 1000)
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1010")
		_, _ = w.Write(append(append([]byte{}, data...), make([]byte, 10)...))
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	_, err := Run(context.Background(), m, e.opts(srv))
	if err == nil || !strings.Contains(err.Error(), "announces 1010 bytes") {
		t.Fatalf("want a size refusal, got %v", err)
	}
	mustRolledBack(t, err)
}

func TestABodyLongerThanDeclaredIsCutOff(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("l"), 1000)
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data) // chunked: no Content-Length to check up front
		w.(http.Flusher).Flush()
		_, _ = w.Write(bytes.Repeat([]byte("!"), 100000))
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	_, err := Run(context.Background(), m, e.opts(srv))
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("want a refusal of the extra bytes, got %v", err)
	}
	mustRolledBack(t, err)
}

func TestAServerErrorIsRetriedAndA404IsNot(t *testing.T) {
	e := newEnv(t)
	data := []byte(strings.Repeat("e", 600))
	calls := 0
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		if calls < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(data)
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/ok", data: data})
	if _, err := Run(context.Background(), m, e.opts(srv)); err != nil {
		t.Fatalf("a 503 should have been retried: %v", err)
	}
	e2 := newEnv(t)
	calls = 0
	m2 := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/missing", data: data})
	_, err := Run(context.Background(), m2, e2.opts(srv))
	if err == nil || calls != 1 {
		t.Fatalf("a 404 must fail at once without retries (calls=%d, err=%v)", calls, err)
	}
}

func TestAFileAlreadyThereWithTheRightHashIsNotDownloaded(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("k"), 700)
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the server was called although the file was already in place")
	}))
	if err := os.MkdirAll(e.paths["models"], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.paths["models"], "voice.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	m := build(t, comp{id: "voice", location: "models", name: "voice.bin", url: srv.URL + "/v", data: data})
	res, err := Run(context.Background(), m, e.opts(srv))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "voice" {
		t.Fatalf("skipped = %v", res.Skipped)
	}
	if o, _ := res.Record.Owner(filepath.Join(e.paths["models"], "voice.bin")); o != record.OwnerAdopted {
		t.Fatalf("a file that was already there must be adopted, not owned: %s", o)
	}
}

func TestADifferentFileWithTheSameNameIsNeverOverwritten(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("n"), 700)
	srv := tlsServer(t, serveBytes(data))
	if err := os.MkdirAll(e.paths["models"], 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(e.paths["models"], "voice.bin")
	if err := os.WriteFile(theirs, []byte("the user's own file"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := build(t, comp{id: "voice", location: "models", name: "voice.bin", url: srv.URL + "/v", data: data})
	_, err := Run(context.Background(), m, e.opts(srv))
	if err == nil || !strings.Contains(err.Error(), "not overwriting") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if b, _ := os.ReadFile(theirs); string(b) != "the user's own file" {
		t.Fatal("the user's file was overwritten")
	}
}

func TestASecondRunOverARecordedInstallationIsRefused(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("r"), 500)
	srv := tlsServer(t, serveBytes(data))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	if _, err := Run(context.Background(), m, e.opts(srv)); err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, e.root)
	_, err := Run(context.Background(), m, e.opts(srv))
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("want ErrAlreadyInstalled, got %v", err)
	}
	if !equal(snap, snapshot(t, e.root)) {
		t.Fatal("the refused second run changed something")
	}
}

func TestCancellingRollsBack(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.root)
	data := bytes.Repeat([]byte("c"), 20000)
	started := make(chan struct{})
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data[:5000])
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := Run(ctx, m, e.opts(srv))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	mustRolledBack(t, err)
	if after := snapshot(t, e.root); !equal(after, before) {
		t.Fatalf("cancel left something behind:\nbefore %v\nafter  %v", before, after)
	}
}

func TestComponentsGoToTheirDestFolder(t *testing.T) {
	e := newEnv(t)
	zipData := makeZip(t, zent{name: "engine.dll", data: []byte("e")})
	srv := tlsServer(t, serveBytes(zipData))
	m := build(t, comp{id: "engine", kind: "zip", location: "install", dest: "engine/v1", url: srv.URL + "/e.zip", data: zipData})
	if _, err := Run(context.Background(), m, e.opts(srv)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.paths["install"], "engine", "v1", "engine.dll")); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFolderChoiceIsAnError(t *testing.T) {
	e := newEnv(t)
	data := []byte("x")
	srv := tlsServer(t, serveBytes(data))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	opt := e.opts(srv)
	delete(opt.Paths, "models")
	if _, err := Run(context.Background(), m, opt); err == nil || !strings.Contains(err.Error(), "no folder was chosen") {
		t.Fatalf("got %v", err)
	}
	opt.Paths = map[string]string{"install": "relative/path", "models": e.paths["models"]}
	if _, err := Run(context.Background(), m, opt); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("got %v", err)
	}
}

func TestAnInstallThroughASymbolicLinkIsRefused(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(e.root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.paths["install"], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.paths["install"], "engine")); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
	zipData := makeZip(t, zent{name: "x.dll", data: []byte("x")})
	srv := tlsServer(t, serveBytes(zipData))
	m := build(t, comp{id: "engine", kind: "zip", location: "install", dest: "engine", url: srv.URL + "/e.zip", data: zipData})
	_, err := Run(context.Background(), m, e.opts(srv))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("something was written through the link: %v", entries)
	}
}

func TestHostileZips(t *testing.T) {
	big := bytes.Repeat([]byte{0}, 64<<20) // compresses to about 64 KiB: a bomb
	cases := map[string]struct {
		entries []zent
		opt     func(*Options)
		want    string
	}{
		"path escaping the folder":  {entries: []zent{{name: "ok.txt", data: []byte("a")}, {name: "../evil.txt", data: []byte("x")}}, want: "not safe"},
		"deep path escaping":        {entries: []zent{{name: "a/../../evil.txt", data: []byte("x")}}, want: "not safe"},
		"absolute path":             {entries: []zent{{name: "/etc/evil", data: []byte("x")}}, want: "not safe"},
		"windows drive":             {entries: []zent{{name: "C:/evil.txt", data: []byte("x")}}, want: "not safe"},
		"backslash":                 {entries: []zent{{name: `..\evil.txt`, data: []byte("x")}}, want: "backslash"},
		"reserved device name":      {entries: []zent{{name: "bin/CON", data: []byte("x")}}, want: "not safe"},
		"symbolic link":             {entries: []zent{{name: "link", data: []byte("/etc"), mode: os.ModeSymlink | 0o777}}, want: "symbolic link"},
		"names that differ in case": {entries: []zent{{name: "a.txt", data: []byte("1")}, {name: "A.TXT", data: []byte("2")}}, want: "differ only in case"},
		"too many entries": {entries: []zent{{name: "1", data: []byte("1")}, {name: "2", data: []byte("1")}, {name: "3", data: []byte("1")}},
			opt: func(o *Options) { o.MaxEntries = 2 }, want: "entries"},
		"unpacks to too much": {entries: []zent{{name: "a", data: bytes.Repeat([]byte("x"), 2000)}},
			opt: func(o *Options) { o.MaxUnpacked = 1000 }, want: "more than the 1000 bytes"},
		"zip bomb": {entries: []zent{{name: "bomb.bin", data: big}}, want: "zip bomb"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			before := snapshot(t, e.root)
			zipData := makeZip(t, tc.entries...)
			srv := tlsServer(t, serveBytes(zipData))
			m := build(t, comp{id: "core", kind: "zip", location: "install", url: srv.URL + "/c.zip", data: zipData})
			opt := e.opts(srv)
			if tc.opt != nil {
				tc.opt(&opt)
			}
			_, err := Run(context.Background(), m, opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
			mustRolledBack(t, err)
			if after := snapshot(t, e.root); !equal(after, before) {
				t.Fatalf("a refused archive left traces:\nbefore %v\nafter  %v", before, after)
			}
			if _, err := os.Stat(filepath.Join(e.root, "evil.txt")); err == nil {
				t.Fatal("a file escaped the install folder")
			}
		})
	}
}

func TestAZipEntryNeverOverwritesAnExistingFile(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.paths["install"], 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(e.paths["install"], "readme.txt")
	if err := os.WriteFile(theirs, []byte("the user's"), 0o644); err != nil {
		t.Fatal(err)
	}
	zipData := makeZip(t, zent{name: "bin/app.exe", data: []byte("a")}, zent{name: "readme.txt", data: []byte("ours")})
	srv := tlsServer(t, serveBytes(zipData))
	m := build(t, comp{id: "core", kind: "zip", location: "install", url: srv.URL + "/c.zip", data: zipData})
	_, err := Run(context.Background(), m, e.opts(srv))
	if err == nil || !strings.Contains(err.Error(), "not overwriting") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if b, _ := os.ReadFile(theirs); string(b) != "the user's" {
		t.Fatal("the user's file was overwritten")
	}
	if _, err := os.Stat(filepath.Join(e.paths["install"], "bin")); err == nil {
		t.Fatal("part of the archive was unpacked before the conflict was found")
	}
}

func TestAnInconsistentContentRangeIsNotTrusted(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.root)
	data := bytes.Repeat([]byte("0123456789"), 5000)
	calls := 0
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data[:20000])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		// Answers the resume with a range that starts somewhere else.
		w.Header().Set("Content-Range", "bytes 0-"+strconv.Itoa(len(data)-1)+"/"+strconv.Itoa(len(data)))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data)
	}))
	m := build(t, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	_, err := Run(context.Background(), m, e.opts(srv))
	if err == nil || !strings.Contains(err.Error(), "Content-Range") {
		t.Fatalf("want a refusal of the inconsistent Content-Range, got %v", err)
	}
	mustRolledBack(t, err)
	if after := snapshot(t, e.root); !equal(after, before) {
		t.Fatalf("left traces: %v", after)
	}
}
