package install

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

// repairRig is an installed demo with two zip components, a single-file model
// and a settings file, on a local HTTPS server that counts requests per path.
type repairRig struct {
	env
	srv    *httptest.Server
	m      *manifest.Manifest
	rec    *record.Record
	mu     sync.Mutex
	hits   map[string]int
	serve  map[string][]byte
	broken map[string]bool // paths that answer 500
}

func newRepairRig(t *testing.T) *repairRig {
	t.Helper()
	rr := &repairRig{env: newEnv(t), hits: map[string]int{}, broken: map[string]bool{}}
	appZip := makeZip(t,
		zent{name: "bin/app.exe", data: []byte("the program")},
		zent{name: "bin/lib.dll", data: []byte("the library")},
		zent{name: "readme.txt", data: []byte("read me")})
	engineZip := makeZip(t, zent{name: "engine.bin", data: bytes.Repeat([]byte("e"), 500)})
	model := bytes.Repeat([]byte("m"), 800)
	rr.serve = map[string][]byte{"/app.zip": appZip, "/engine.zip": engineZip, "/model.bin": model}
	rr.srv = tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rr.mu.Lock()
		rr.hits[req.URL.Path]++
		bad := rr.broken[req.URL.Path]
		data := rr.serve[req.URL.Path]
		rr.mu.Unlock()
		if bad || data == nil {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, req, "x", time.Time{}, bytes.NewReader(data))
	}))
	rr.m = buildX(t, withPortConfig,
		comp{id: "app", kind: "zip", location: "install", url: rr.srv.URL + "/app.zip", data: appZip},
		comp{id: "engine", kind: "zip", location: "install", dest: "engine", url: rr.srv.URL + "/engine.zip", data: engineZip},
		comp{id: "model", location: "models", name: "m.bin", url: rr.srv.URL + "/model.bin", data: model})
	opt := rr.opts(rr.srv)
	opt.Answers = map[string]int{"port": 5099}
	res, err := Run(context.Background(), rr.m, opt)
	if err != nil {
		t.Fatal(err)
	}
	rr.rec = res.Record
	rr.hits = map[string]int{}
	return rr
}

func (rr *repairRig) file(parts ...string) string {
	return filepath.Join(append([]string{rr.paths["install"]}, parts...)...)
}

// reload reads the record back from disk, as the command line does.
func (rr *repairRig) reload(t *testing.T) *record.Record {
	t.Helper()
	r, err := record.Load(rr.env.rec)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (rr *repairRig) repair(t *testing.T) (*RepairResult, error) {
	t.Helper()
	return Repair(context.Background(), rr.m, rr.reload(t), rr.opts(rr.srv))
}

func (rr *repairRig) verify(t *testing.T) []Problem {
	t.Helper()
	ps, _, err := Verify(rr.m, rr.reload(t), rr.opts(rr.srv))
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestAnIntactInstallationHasNothingToRepair(t *testing.T) {
	rr := newRepairRig(t)
	ps, checked, err := Verify(rr.m, rr.reload(t), rr.opts(rr.srv))
	if err != nil || len(ps) != 0 {
		t.Fatalf("problems %+v, err %v", ps, err)
	}
	if checked != 6 { // three files of app, one of engine, the model and config.json
		t.Fatalf("checked %d files, want 6", checked)
	}
	res, err := rr.repair(t)
	if err != nil || len(res.Fixed) != 0 || len(res.Failed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(rr.hits) != 0 {
		t.Fatalf("an intact installation downloaded %v", rr.hits)
	}
}

func TestRepairPutsBackOnlyWhatIsDamagedAndDownloadsOnlyThat(t *testing.T) {
	rr := newRepairRig(t)
	// Damage: one file deleted, one changed (same size, other content), the model
	// truncated; the engine is intact. The user's own file and an edited settings
	// file are not damage.
	if err := os.Remove(rr.file("bin", "lib.dll")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rr.file("bin", "app.exe"), []byte("the PROGRAM"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rr.paths["models"], "m.bin"), []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := rr.file("bin", "my-notes.txt")
	if err := os.WriteFile(mine, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rr.file("config.json"), []byte(`{"port": 6000}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ps := rr.verify(t)
	kinds := map[string]ProblemKind{}
	for _, p := range ps {
		kinds[filepath.Base(p.Path)] = p.Kind
		if !p.Fixable {
			t.Errorf("%s should be fixable", p.Path)
		}
	}
	want := map[string]ProblemKind{"lib.dll": ProblemMissing, "app.exe": ProblemChanged, "m.bin": ProblemChanged}
	if len(kinds) != len(want) {
		t.Fatalf("problems %v, want %v", kinds, want)
	}
	for k, v := range want {
		if kinds[k] != v {
			t.Fatalf("problems %v, want %v", kinds, want)
		}
	}

	res, err := rr.repair(t)
	if err != nil || len(res.Failed) != 0 || len(res.Fixed) != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := os.ReadFile(rr.file("bin", "lib.dll")); string(got) != "the library" {
		t.Fatalf("lib.dll = %q", got)
	}
	if got, _ := os.ReadFile(rr.file("bin", "app.exe")); string(got) != "the program" {
		t.Fatalf("app.exe = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(rr.paths["models"], "m.bin")); !bytes.Equal(got, rr.serve["/model.bin"]) {
		t.Fatal("the model was not restored")
	}
	// What is the user's is untouched, and the intact component was not fetched.
	if got, _ := os.ReadFile(mine); string(got) != "mine" {
		t.Fatalf("the user's file: %q", got)
	}
	if got, _ := os.ReadFile(rr.file("config.json")); string(got) != `{"port": 6000}` {
		t.Fatalf("edited settings were overwritten: %q", got)
	}
	if rr.hits["/engine.zip"] != 0 || rr.hits["/app.zip"] == 0 || rr.hits["/model.bin"] == 0 {
		t.Fatalf("downloads: %v", rr.hits)
	}
	if ps := rr.verify(t); len(ps) != 0 {
		t.Fatalf("still damaged after repair: %+v", ps)
	}
	for _, s := range snapshot(t, rr.root) {
		if strings.Contains(s, ".part") || strings.Contains(s, ".stellatinus-tmp") {
			t.Errorf("left behind: %s", s)
		}
	}
	// The record is still true: removing the installation leaves only the
	// user's own file and what was not the installer's.
	r := rr.reload(t)
	rep := r.Apply(r.Plan(record.Choices{}))
	for _, f := range rep.Failed {
		if !strings.Contains(f.Err.Error(), "not empty") {
			t.Errorf("removal: %s: %v", f.Path, f.Err)
		}
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatal("removal after a repair took the user's file")
	}
}

func TestADeletedSettingsFileIsWrittenAgainWithTheRecordedAnswer(t *testing.T) {
	rr := newRepairRig(t)
	if err := os.Remove(rr.file("config.json")); err != nil {
		t.Fatal(err)
	}
	if ps := rr.verify(t); len(ps) != 1 || ps[0].Kind != ProblemMissing || ps[0].Component != "config" {
		t.Fatalf("%+v", ps)
	}
	res, err := rr.repair(t)
	if err != nil || len(res.Fixed) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := os.ReadFile(rr.file("config.json")); !strings.Contains(string(got), "5099") {
		t.Fatalf("config.json = %q, want the port that was chosen, not the default", got)
	}
	if len(rr.hits) != 0 {
		t.Fatalf("a settings file needs no download: %v", rr.hits)
	}
}

func TestRepairNeverTouchesAConflict(t *testing.T) {
	rr := newRepairRig(t)
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := rr.file("bin", "lib.dll")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, target); err != nil {
		t.Skip("symbolic links are not available here")
	}
	// A folder where a file goes is a conflict too.
	if err := os.Remove(rr.file("readme.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rr.file("readme.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	ps := rr.verify(t)
	if len(ps) != 2 {
		t.Fatalf("%+v", ps)
	}
	for _, p := range ps {
		if p.Kind != ProblemConflict || p.Fixable {
			t.Errorf("%+v must be an unfixable conflict", p)
		}
	}
	res, err := rr.repair(t)
	if err != nil || len(res.Fixed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "not ours" {
		t.Fatalf("a file reached through a link was changed: %q", got)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}
	if len(rr.hits) != 0 {
		t.Fatalf("conflicts need no download: %v", rr.hits)
	}
}

func TestRepairThroughAFolderThatBecameALinkIsRefused(t *testing.T) {
	rr := newRepairRig(t)
	outside := t.TempDir()
	if err := os.RemoveAll(rr.file("bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, rr.file("bin")); err != nil {
		t.Skip("symbolic links are not available here")
	}
	// The files under the link are conflicts, not "missing": verifying must not
	// follow the link, and repair must not try.
	for _, p := range rr.verify(t) {
		if strings.HasPrefix(p.Path, rr.file("bin")) && (p.Kind != ProblemConflict || p.Fixable) {
			t.Errorf("%+v must be an unfixable conflict", p)
		}
	}
	res, err := rr.repair(t)
	if err != nil || len(res.Fixed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("repair wrote through a link into %s: %v", outside, entries)
	}
}

func TestACorruptDownloadFailsTheRepairAndLeavesTheFileAsItWas(t *testing.T) {
	rr := newRepairRig(t)
	rr.mu.Lock()
	rr.serve["/model.bin"] = bytes.Repeat([]byte("x"), 800) // right size, wrong hash
	rr.mu.Unlock()
	model := filepath.Join(rr.paths["models"], "m.bin")
	if err := os.WriteFile(model, []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(rr.file("bin", "lib.dll")); err != nil {
		t.Fatal(err)
	}
	res, err := rr.repair(t)
	if err != nil {
		t.Fatal(err)
	}
	// The model fails; the other component is still repaired.
	if len(res.Failed) != 1 || filepath.Base(res.Failed[0].Path) != "m.bin" || len(res.Fixed) != 1 {
		t.Fatalf("%+v", res)
	}
	if got, _ := os.ReadFile(model); string(got) != "damaged" {
		t.Fatalf("a failed repair changed the file: %q", got)
	}
	for _, s := range snapshot(t, rr.root) {
		if strings.Contains(s, ".part") {
			t.Errorf("left behind: %s", s)
		}
	}
}

func TestAnOfflineRepairReportsAndChangesNothingElse(t *testing.T) {
	rr := newRepairRig(t)
	rr.mu.Lock()
	rr.broken["/app.zip"] = true
	rr.mu.Unlock()
	if err := os.Remove(rr.file("bin", "lib.dll")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, rr.root)
	res, err := rr.repair(t)
	if err != nil || len(res.Failed) != 1 || len(res.Fixed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if after := snapshot(t, rr.root); !equal(after, before) {
		t.Fatalf("a failed repair changed the folders:\nbefore %v\nafter  %v", before, after)
	}
}

func TestRepairIsRefusedForTheWrongRecord(t *testing.T) {
	rr := newRepairRig(t)
	other := *rr.reload(t)
	other.AppVersion = "0.9.0"
	if _, err := Repair(context.Background(), rr.m, &other, rr.opts(rr.srv)); !errors.Is(err, ErrDifferentVersion) {
		t.Fatalf("got %v, want ErrDifferentVersion", err)
	}
	other = *rr.reload(t)
	other.AppID = "someone-else"
	if _, err := Repair(context.Background(), rr.m, &other, rr.opts(rr.srv)); err == nil {
		t.Fatal("a record of another app must be refused")
	}
	other = *rr.reload(t)
	other.Paths = nil
	if _, err := Repair(context.Background(), rr.m, &other, rr.opts(rr.srv)); !errors.Is(err, ErrNoPaths) {
		t.Fatalf("got %v, want ErrNoPaths", err)
	}
	if len(rr.hits) != 0 {
		t.Fatalf("a refused repair downloaded %v", rr.hits)
	}
}

func TestRepairStopsWhenCancelled(t *testing.T) {
	rr := newRepairRig(t)
	if err := os.Remove(rr.file("bin", "lib.dll")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Repair(ctx, rr.m, rr.reload(t), rr.opts(rr.srv)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if len(rr.hits) != 0 {
		t.Fatalf("downloaded after cancelling: %v", rr.hits)
	}
}
