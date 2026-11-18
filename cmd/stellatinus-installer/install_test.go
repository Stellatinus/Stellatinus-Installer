package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

func shaOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(files[n]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rig is a local HTTPS server, a manifest that points at it and folders to
// install into. The configuration folder (where the record lives) is separate
// from the folders that are checked for leftovers.
type rig struct {
	t        *testing.T
	srv      *httptest.Server
	hits     atomic.Int32
	onHit    func(path string) // called before a request is served
	manifest string
	root     string
	app      string
	models   string
}

func newRig(t *testing.T, serve map[string][]byte, declared map[string][]byte) *rig {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("AppData", cfg)
	t.Setenv("HOME", cfg)
	r := &rig{t: t, root: t.TempDir()}
	mux := http.NewServeMux()
	for path, data := range serve {
		data := data
		mux.HandleFunc(path, func(w http.ResponseWriter, req *http.Request) {
			r.hits.Add(1)
			if r.onHit != nil {
				r.onHit(path)
			}
			http.ServeContent(w, req, "x", time.Time{}, bytes.NewReader(data))
		})
	}
	r.srv = httptest.NewTLSServer(mux)
	t.Cleanup(r.srv.Close)
	r.app, r.models = filepath.Join(r.root, "app"), filepath.Join(r.root, "models")

	coreZip, model := declared["/core.zip"], declared["/m.bin"]
	doc := map[string]any{
		"schema_version": 1,
		"app":            map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "accent": "#2f6df6"},
		"allow_machine":  false,
		"locations": []any{
			map[string]any{"id": "install", "label": "Program", "kind": "install", "required": true,
				"default": map[string]any{"linux": "${HOME}/demo", "windows": "${HOME}/demo", "darwin": "${HOME}/demo"}},
			map[string]any{"id": "models", "label": "Models", "kind": "models", "adopt": true,
				"default": map[string]any{"linux": "${HOME}/models", "windows": "${HOME}/models", "darwin": "${HOME}/models"}},
		},
		"components": []any{
			map[string]any{"id": "core", "label": "the core", "kind": "zip", "url": r.srv.URL + "/core.zip",
				"sha256": shaOf(coreZip), "size": len(coreZip), "location": "install"},
			map[string]any{"id": "model", "label": "the model", "name": "m.bin", "url": r.srv.URL + "/m.bin",
				"sha256": shaOf(model), "size": len(model), "location": "models"},
		},
	}
	b, _ := json.Marshal(doc)
	r.manifest = filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(r.manifest, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

func goodRig(t *testing.T) *rig {
	core := zipOf(t, map[string]string{"bin/app.exe": "binary", "readme.txt": "hello"})
	model := bytes.Repeat([]byte("m"), 4000)
	files := map[string][]byte{"/core.zip": core, "/m.bin": model}
	return newRig(t, files, files)
}

func (r *rig) args(extra ...string) []string {
	a := []string{"install", "--set", "install=" + r.app, "--set", "models=" + r.models}
	a = append(a, extra...)
	return append(a, r.manifest)
}

func (r *rig) run(args ...string) (int, string, string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code := runWith(context.Background(), deps{client: r.srv.Client()}, args, &out, &errb)
	return code, out.String(), errb.String()
}

func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func same(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

func TestInstallThenUninstallLeavesNothing(t *testing.T) {
	r := goodRig(t)
	before := tree(t, r.root)
	code, out, errOut := r.run(r.args()...)
	if code != exitOK {
		t.Fatalf("install exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "Installed Demo 1.0.0") || !strings.Contains(out, "Downloading the core") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(r.app, "bin", "app.exe")); string(b) != "binary" {
		t.Fatal("the zip was not unpacked")
	}
	if _, err := os.Stat(filepath.Join(r.models, "m.bin")); err != nil {
		t.Fatal(err)
	}

	// Without --yes only the plan is shown.
	code, out, _ = r.run("uninstall", "demo")
	if code != exitOK || !strings.Contains(out, "Nothing was removed") {
		t.Fatalf("uninstall without --yes: %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(r.app, "readme.txt")); err != nil {
		t.Fatal("uninstall without --yes removed something")
	}

	code, out, errOut = r.run("uninstall", "--yes", "demo")
	if code != exitOK {
		t.Fatalf("uninstall --yes exit %d\n%s%s", code, out, errOut)
	}
	if after := tree(t, r.root); !same(after, before) {
		t.Fatalf("not clean after uninstall:\nbefore %v\nafter  %v", before, after)
	}
	// The record is gone too, so a second uninstall has nothing to do.
	if code, _, e := r.run("uninstall", "--yes", "demo"); code != exitProblem || !strings.Contains(e, "nothing to remove") {
		t.Fatalf("second uninstall: %d %s", code, e)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	r := goodRig(t)
	before := tree(t, r.root)
	code, out, errOut := r.run(r.args("--dry-run")...)
	if code != exitOK || !strings.Contains(out, "Dry run") {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	if r.hits.Load() != 0 {
		t.Fatal("a dry run downloaded something")
	}
	if !same(tree(t, r.root), before) {
		t.Fatal("a dry run changed the folders")
	}
}

func TestJSONOutputIsOneEventPerLine(t *testing.T) {
	r := goodRig(t)
	code, out, errOut := r.run(r.args("--json")...)
	if code != exitOK {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	kinds := map[string]int{}
	var last map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSON: %q (%v)", line, err)
		}
		kinds[ev["event"].(string)]++
		last = ev
	}
	if kinds["check"] == 0 || kinds["progress"] == 0 || kinds["result"] != 1 {
		t.Fatalf("events: %v", kinds)
	}
	if last["event"] != "result" || last["ok"] != true || last["exit"].(float64) != 0 {
		t.Fatalf("the last event must be the result: %v", last)
	}
}

func TestSilentPrintsNothingOnSuccess(t *testing.T) {
	r := goodRig(t)
	code, out, errOut := r.run(r.args("--silent")...)
	if code != exitOK || out != "" || errOut != "" {
		t.Fatalf("silent: %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestAFailedInstallRollsBackWithExit3(t *testing.T) {
	core := zipOf(t, map[string]string{"a.txt": "a"})
	model := bytes.Repeat([]byte("m"), 4000)
	// The server sends other bytes than the manifest declares.
	r := newRig(t,
		map[string][]byte{"/core.zip": core, "/m.bin": bytes.Repeat([]byte("X"), 4000)},
		map[string][]byte{"/core.zip": core, "/m.bin": model})
	before := tree(t, r.root)
	code, _, errOut := r.run(r.args()...)
	if code != exitFailedRolledBack || !strings.Contains(errOut, "rolled back") {
		t.Fatalf("want exit 3 and a rollback message, got %d: %s", code, errOut)
	}
	if after := tree(t, r.root); !same(after, before) {
		t.Fatalf("a failed install left traces:\nbefore %v\nafter  %v", before, after)
	}
	var res map[string]any
	_, out, _ := r.run(r.args("--json")...)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil || res["ok"] != false || res["rolled_back"] != true || res["exit"].(float64) != 3 {
		t.Fatalf("the JSON result of a failed install: %v (%v)", res, err)
	}
}

func TestCancellingRollsBackWithExit3(t *testing.T) {
	r := goodRig(t)
	before := tree(t, r.root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // as if Ctrl+C came at once
	var out, errb bytes.Buffer
	code := runWith(ctx, deps{client: r.srv.Client()}, r.args(), &out, &errb)
	if code != exitFailedRolledBack {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !same(tree(t, r.root), before) {
		t.Fatal("a cancelled install left traces")
	}
}

func TestAFailedCheckStopsBeforeAnything(t *testing.T) {
	r := goodRig(t)
	f := filepath.Join(r.root, "a-file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, r.root)
	code, out, errOut := r.run("install", "--set", "install="+f, "--set", "models="+r.models, r.manifest)
	if code != exitProblem || !strings.Contains(out, "FAIL") || !strings.Contains(errOut, "nothing was installed") {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	if r.hits.Load() != 0 || !same(tree(t, r.root), before) {
		t.Fatal("something happened although a check failed")
	}
}

func TestASecondInstallIsRefused(t *testing.T) {
	r := goodRig(t)
	if code, out, e := r.run(r.args()...); code != exitProblem && code != exitOK {
		t.Fatalf("%d %s%s", code, out, e)
	}
	before := tree(t, r.root)
	code, out, errOut := r.run(r.args()...)
	if code != exitProblem || !strings.Contains(out+errOut, "already") {
		t.Fatalf("second install: %d\n%s%s", code, out, errOut)
	}
	if !same(tree(t, r.root), before) {
		t.Fatal("the refused second install changed something")
	}
}

func TestResponseFileAndPrecedence(t *testing.T) {
	r := goodRig(t)
	other := filepath.Join(r.root, "from-set")
	resp := filepath.Join(t.TempDir(), "answers.json")
	b, _ := json.Marshal(map[string]any{"locations": map[string]string{"install": filepath.Join(r.root, "from-response"), "models": r.models}})
	if err := os.WriteFile(resp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	// --set wins over the response file.
	code, out, errOut := r.run("install", "--response", resp, "--set", "install="+other, "--silent", r.manifest)
	if code != exitOK {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(other, "readme.txt")); err != nil {
		t.Fatal("--set did not win over the response file")
	}
	if _, err := os.Stat(filepath.Join(r.root, "from-response")); err == nil {
		t.Fatal("the response file's folder was used although --set was given")
	}
}

func TestBadResponseFiles(t *testing.T) {
	r := goodRig(t)
	for name, body := range map[string]string{
		"unknown field":    `{"shortcuts": {"desktop": true}}`,
		"unknown location": `{"locations": {"nowhere": "/x"}}`,
		"not json":         `install please`,
		"trailing data":    `{} {}`,
	} {
		p := filepath.Join(t.TempDir(), "r.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := r.run("install", "--response", p, r.manifest)
		if code == exitOK || errOut == "" {
			t.Errorf("%s: accepted (exit %d)", name, code)
		}
	}
	if code, _, _ := r.run("install", "--response", filepath.Join(t.TempDir(), "missing.json"), r.manifest); code == exitOK {
		t.Error("a missing response file was accepted")
	}
}

func TestInstallUsageErrors(t *testing.T) {
	r := goodRig(t)
	for name, args := range map[string][]string{
		"no manifest":      {"install"},
		"bad --set":        {"install", "--set", "nonsense", r.manifest},
		"unknown location": {"install", "--set", "bogus=/x", r.manifest},
		"bad scope":        {"install", "--scope", "everyone", r.manifest},
		"unknown flag":     {"install", "--frobnicate", r.manifest},
	} {
		if code, _, _ := r.run(args...); code != exitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, exitUsage)
		}
	}
	// The manifest does not allow an installation for all users.
	code, _, errOut := r.run("install", "--scope", "machine", r.manifest)
	if code != exitProblem || !strings.Contains(errOut, "all users") {
		t.Errorf("machine scope: %d %s", code, errOut)
	}
}

func TestUninstallWithoutARecord(t *testing.T) {
	r := goodRig(t)
	code, _, errOut := r.run("uninstall", "--yes", "demo")
	if code != exitProblem || !strings.Contains(errOut, "nothing to remove") {
		t.Fatalf("%d %s", code, errOut)
	}
	if code, _, _ := r.run("uninstall"); code != exitUsage {
		t.Fatalf("uninstall without an argument: %d", code)
	}
}

func TestUninstallNeverTouchesWhatIsNotTheInstallers(t *testing.T) {
	r := goodRig(t)
	if err := os.MkdirAll(r.models, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(r.models, "whisper-large-v3")
	if err := os.WriteFile(mine, []byte("the user's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, e := r.run(r.args("--silent")...); code != exitOK {
		t.Fatalf("%d %s%s", code, out, e)
	}
	if code, out, e := r.run("uninstall", "--yes", "--silent", "demo"); code != exitOK {
		t.Fatalf("%d %s%s", code, out, e)
	}
	if b, _ := os.ReadFile(mine); string(b) != "the user's" {
		t.Fatal("the user's model was removed")
	}
	if got := tree(t, r.models); !same(got, []string{".", "whisper-large-v3"}) {
		t.Fatalf("models folder after uninstall: %v", got)
	}
	if _, err := os.Stat(r.app); err == nil {
		t.Fatal("the install folder is still there")
	}
}

func TestUninstallKeepsWhatTheUserAsksToKeep(t *testing.T) {
	r := goodRig(t)
	if code, out, e := r.run(r.args("--silent")...); code != exitOK {
		t.Fatalf("%d %s%s", code, out, e)
	}
	if code, out, e := r.run("uninstall", "--yes", "--silent", "--keep-location", "models", "demo"); code != exitOK {
		t.Fatalf("%d %s%s", code, out, e)
	}
	if _, err := os.Stat(filepath.Join(r.models, "m.bin")); err != nil {
		t.Fatal("--keep-location did not keep the model")
	}
	if _, err := os.Stat(filepath.Join(r.app, "readme.txt")); err == nil {
		t.Fatal("the program was not removed")
	}
}

func TestUninstallReportsLeftoversAndCanBeRepeated(t *testing.T) {
	r := goodRig(t)
	if code, out, e := r.run(r.args("--silent")...); code != exitOK {
		t.Fatalf("%d %s%s", code, out, e)
	}
	notes := filepath.Join(r.app, "notes.txt") // made later by the user: not in the record
	if err := os.WriteFile(notes, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := r.run("uninstall", "--yes", "--silent", "demo")
	if code != exitLeftovers || !strings.Contains(errOut, "not empty") {
		t.Fatalf("want exit 5 and the reason, got %d: %s", code, errOut)
	}
	if b, _ := os.ReadFile(notes); string(b) != "mine" {
		t.Fatal("the user's file was touched")
	}
	// The record now lists only what is left, so the job can be finished.
	recPath, perr := record.DefaultPath("demo", "user")
	if perr != nil {
		t.Fatal(perr)
	}
	left, lerr := record.Load(recPath)
	if lerr != nil {
		t.Fatalf("the record must be kept after leftovers: %v", lerr)
	}
	if len(left.Entries) != 1 || left.Entries[0].Path != r.app {
		t.Fatalf("the record should list only the folder that is left, got %v", left.Entries)
	}
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	if code, _, e := r.run("uninstall", "--yes", "--silent", "demo"); code != exitOK {
		t.Fatalf("second try: %d %s", code, e)
	}
	if _, err := os.Stat(r.app); err == nil {
		t.Fatal("the install folder is still there")
	}
}

func TestARollbackThatCannotFinishExitsWith4AndKeepsTheRecord(t *testing.T) {
	core := zipOf(t, map[string]string{"bin/app.exe": "binary"})
	model := bytes.Repeat([]byte("m"), 4000)
	r := newRig(t,
		map[string][]byte{"/core.zip": core, "/m.bin": bytes.Repeat([]byte("X"), 4000)}, // wrong bytes: the install fails
		map[string][]byte{"/core.zip": core, "/m.bin": model})
	stray := filepath.Join(r.app, "stray.txt")
	r.onHit = func(path string) {
		if path == "/m.bin" { // the user drops a file into the install folder while it runs
			_ = os.WriteFile(stray, []byte("not ours"), 0o644)
		}
	}
	code, _, errOut := r.run(r.args()...)
	if code != exitFailedPartial || !strings.Contains(errOut, "could not remove") || !strings.Contains(errOut, "uninstall --yes") {
		t.Fatalf("want exit 4 with what is left and how to finish, got %d: %s", code, errOut)
	}
	if b, _ := os.ReadFile(stray); string(b) != "not ours" {
		t.Fatal("the user's file was touched")
	}
	recPath, err := record.DefaultPath("demo", "user")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := record.Load(recPath)
	if err != nil {
		t.Fatalf("the record must be kept so the job can be finished: %v", err)
	}
	if len(rec.Entries) == 0 || len(rec.Entries) > 3 {
		t.Fatalf("the record should list only what is left, has %d entries: %v", len(rec.Entries), rec.Entries)
	}
}

// withPort rewrites the rig's manifest to ask for a port and write it to a
// config file in the install folder.
func (r *rig) withPort(def int) {
	r.t.Helper()
	b, err := os.ReadFile(r.manifest)
	if err != nil {
		r.t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		r.t.Fatal(err)
	}
	doc["options"] = []any{map[string]any{"id": "port", "label": "Port", "kind": "port", "default": def}}
	doc["config"] = []any{map[string]any{"location": "install", "name": "config.json", "format": "json", "values": map[string]any{"port": "port"}}}
	b, _ = json.Marshal(doc)
	if err := os.WriteFile(r.manifest, b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen here: %v", err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

func TestAnOptionIsAnsweredWrittenAndRemoved(t *testing.T) {
	r := goodRig(t)
	r.withPort(5080)
	port := freePort(t)
	before := tree(t, r.root)
	code, out, errOut := r.run(r.args("--option", "port="+strconv.Itoa(port))...)
	if code != exitOK {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	b, err := os.ReadFile(filepath.Join(r.app, "config.json"))
	if err != nil || !strings.Contains(string(b), strconv.Itoa(port)) {
		t.Fatalf("config.json = %s (%v)", b, err)
	}
	if !strings.Contains(out, "Port 5080") && !strings.Contains(out, "is free") {
		t.Fatalf("the port check was not shown:\n%s", out)
	}
	if code, _, e := r.run("uninstall", "--yes", "--silent", "demo"); code != exitOK {
		t.Fatalf("%d %s", code, e)
	}
	if !same(tree(t, r.root), before) {
		t.Fatal("the config file was left behind")
	}
}

func TestABusyPortStopsBeforeAnything(t *testing.T) {
	r := goodRig(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen here: %v", err)
	}
	defer func() { _ = ln.Close() }()
	busy := ln.Addr().(*net.TCPAddr).Port
	r.withPort(busy) // the default itself is taken
	before := tree(t, r.root)
	code, out, errOut := r.run(r.args()...)
	if code != exitProblem || !strings.Contains(out, "FAIL") || !strings.Contains(out, "cannot be used") || !strings.Contains(errOut, "nothing was installed") {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	if r.hits.Load() != 0 || !same(tree(t, r.root), before) {
		t.Fatal("something happened although the port was busy")
	}
}

func TestBadOptionFlagsAreUsageErrors(t *testing.T) {
	r := goodRig(t)
	r.withPort(5080)
	for name, extra := range map[string][]string{
		"not a number":   {"--option", "port=abc"},
		"no equals sign": {"--option", "port"},
		"unknown option": {"--option", "nope=1"},
	} {
		if code, _, _ := r.run(r.args(extra...)...); code != exitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, exitUsage)
		}
	}
	// A privileged port is refused by the engine before any download.
	if code, _, _ := r.run(r.args("--option", "port=80")...); code == exitOK {
		t.Errorf("port 80 accepted (exit %d)", code)
	}
	if r.hits.Load() != 0 {
		t.Error("a bad answer downloaded something")
	}
}
