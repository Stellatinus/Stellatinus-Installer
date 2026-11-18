package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installed(t *testing.T) *rig {
	t.Helper()
	r := goodRig(t)
	if code, out, e := r.run(r.args()...); code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, e)
	}
	r.hits.Store(0)
	return r
}

func (r *rig) repairArgs(extra ...string) []string {
	return append(append([]string{"repair"}, extra...), r.manifest)
}

func TestRepairOfAnIntactInstallationDoesNothing(t *testing.T) {
	r := installed(t)
	before := tree(t, r.root)
	code, out, e := r.run(r.repairArgs()...)
	if code != exitOK || !strings.Contains(out, "Nothing to repair") {
		t.Fatalf("%d\n%s%s", code, out, e)
	}
	if r.hits.Load() != 0 || !same(tree(t, r.root), before) {
		t.Fatal("an intact installation was touched")
	}
}

func TestRepairShowsTheProblemsAndOnlyFixesWithYes(t *testing.T) {
	r := installed(t)
	if err := os.Remove(filepath.Join(r.app, "bin", "app.exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.models, "m.bin"), bytes.Repeat([]byte("x"), 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	before := tree(t, r.root)

	// Without --yes: the problems are listed, nothing changes, exit 1.
	code, out, e := r.run(r.repairArgs()...)
	if code != exitProblem || !strings.Contains(out, "missing") || !strings.Contains(out, "changed") || !strings.Contains(out, "Run again with --yes") {
		t.Fatalf("%d\n%s%s", code, out, e)
	}
	if r.hits.Load() != 0 || !same(tree(t, r.root), before) {
		t.Fatal("repair without --yes changed something or downloaded")
	}
	if _, err := os.Stat(filepath.Join(r.app, "bin", "app.exe")); err == nil {
		t.Fatal("repair without --yes restored a file")
	}

	code, out, e = r.run(r.repairArgs("--yes")...)
	if code != exitOK || !strings.Contains(out, "Repaired 2 file(s)") || !strings.Contains(out, "Downloading") {
		t.Fatalf("%d\n%s%s", code, out, e)
	}
	if b, _ := os.ReadFile(filepath.Join(r.app, "bin", "app.exe")); string(b) != "binary" {
		t.Fatalf("app.exe = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(r.models, "m.bin")); !bytes.Equal(b, bytes.Repeat([]byte("m"), 4000)) {
		t.Fatal("the model was not restored")
	}
	if code, out, e := r.run(r.repairArgs()...); code != exitOK || !strings.Contains(out, "Nothing to repair") {
		t.Fatalf("after the repair: %d\n%s%s", code, out, e)
	}
	// And the installation can still be removed cleanly.
	if code, out, e := r.run("uninstall", "--yes", "demo"); code != exitOK {
		t.Fatalf("uninstall after repair: %d\n%s%s", code, out, e)
	}
}

func TestRepairWithoutAnInstallationOrForAnotherVersion(t *testing.T) {
	r := goodRig(t)
	if code, _, e := r.run(r.repairArgs("--yes")...); code != exitProblem || !strings.Contains(e, "nothing to repair") {
		t.Fatalf("not installed: %d %s", code, e)
	}
	if r.hits.Load() != 0 {
		t.Fatal("downloaded")
	}

	r = installed(t)
	b, _ := os.ReadFile(r.manifest)
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["app"].(map[string]any)["version"] = "2.0.0"
	b, _ = json.Marshal(doc)
	if err := os.WriteFile(r.manifest, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(r.app, "readme.txt")); err != nil {
		t.Fatal(err)
	}
	if code, _, e := r.run(r.repairArgs("--yes")...); code != exitProblem || !strings.Contains(e, "does not update") {
		t.Fatalf("other version: %d %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(r.app, "readme.txt")); err == nil || r.hits.Load() != 0 {
		t.Fatal("a repair with another version's manifest changed something")
	}
}

func TestRepairThatCannotDownloadExitsWith6(t *testing.T) {
	r := installed(t)
	if err := os.Remove(filepath.Join(r.app, "readme.txt")); err != nil {
		t.Fatal(err)
	}
	// The package is no longer where the manifest says: a 404, which is not retried.
	b, _ := os.ReadFile(r.manifest)
	if err := os.WriteFile(r.manifest, bytes.ReplaceAll(b, []byte("/core.zip"), []byte("/gone.zip")), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, e := r.run(r.repairArgs("--yes")...)
	if code != exitRepairLeft || !strings.Contains(e, "could not be put back") {
		t.Fatalf("%d %s", code, e)
	}
}

func TestRepairLeavesAConflictAndSaysSo(t *testing.T) {
	r := installed(t)
	f := filepath.Join(r.app, "readme.txt")
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only a conflict: nothing can be done, exit 1 even with --yes, no download.
	code, out, e := r.run(r.repairArgs("--yes")...)
	if code != exitProblem || !strings.Contains(out, "conflict") || r.hits.Load() != 0 {
		t.Fatalf("%d\n%s%s", code, out, e)
	}
	// A conflict and a repairable file: the file is put back, exit 6 for the rest.
	if err := os.Remove(filepath.Join(r.app, "bin", "app.exe")); err != nil {
		t.Fatal(err)
	}
	code, out, e = r.run(r.repairArgs("--yes")...)
	if code != exitRepairLeft || !strings.Contains(out, "Repaired 1 file(s)") || !strings.Contains(e, "something else stands") {
		t.Fatalf("%d\n%s%s", code, out, e)
	}
	if info, err := os.Stat(f); err != nil || !info.IsDir() {
		t.Fatal("the conflicting folder was touched")
	}
}

func TestRepairJSONEvents(t *testing.T) {
	r := installed(t)
	if err := os.Remove(filepath.Join(r.app, "readme.txt")); err != nil {
		t.Fatal(err)
	}
	code, out, e := r.run(r.repairArgs("--json", "--yes")...)
	if code != exitOK {
		t.Fatalf("%d\n%s%s", code, out, e)
	}
	var kinds []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev struct {
			Event    string `json:"event"`
			Repaired int    `json:"repaired"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		kinds = append(kinds, ev.Event)
		if ev.Event == "result" && ev.Repaired != 1 {
			t.Fatalf("result: %s", line)
		}
	}
	if kinds[0] != "problem" || kinds[len(kinds)-1] != "result" {
		t.Fatalf("events %v: want a problem first and the result last", kinds)
	}
}

func TestRepairStopsWhenCancelled(t *testing.T) {
	r := installed(t)
	if err := os.Remove(filepath.Join(r.app, "readme.txt")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errb bytes.Buffer
	code := runWith(ctx, deps{client: r.srv.Client()}, r.repairArgs("--yes"), &out, &errb)
	if code != exitRepairLeft || !strings.Contains(errb.String(), "run it again") {
		t.Fatalf("%d\n%s%s", code, out.String(), errb.String())
	}
}
