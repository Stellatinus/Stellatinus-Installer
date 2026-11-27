package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

func logDir(t *testing.T) string {
	t.Helper()
	p, err := record.DefaultPath("demo", "user")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(filepath.Dir(p), "log")
}

// readLogs returns the entries of each log file, oldest first.
func readLogs(t *testing.T) [][]map[string]any {
	t.Helper()
	ents, _ := os.ReadDir(logDir(t))
	var out [][]map[string]any
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(logDir(t), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var lines []map[string]any
		for _, ln := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			var m map[string]any
			if err := json.Unmarshal(ln, &m); err != nil {
				t.Fatalf("%s: not JSON: %q", e.Name(), ln)
			}
			lines = append(lines, m)
		}
		out = append(out, lines)
	}
	return out
}

func has(lines []map[string]any, kind string) map[string]any {
	for _, l := range lines {
		if l["event"] == kind {
			return l
		}
	}
	return nil
}

func lastResult(t *testing.T, lines []map[string]any) map[string]any {
	t.Helper()
	l := lines[len(lines)-1]
	if l["event"] != "result" {
		t.Fatalf("the log must end with the result, ends with %v", l)
	}
	return l
}

func TestInstallLogsASuccessAndShowsItsPath(t *testing.T) {
	r := goodRig(t)
	code, out, e := r.run(r.args()...)
	if code != exitOK || !strings.Contains(out, "Log: "+logDir(t)) {
		t.Fatalf("%d\n%s%s", code, out, e)
	}
	logs := readLogs(t)
	if len(logs) != 1 {
		t.Fatalf("want one log, got %d", len(logs))
	}
	st := has(logs[0], "start")
	if st == nil || st["command"] != "install" || st["version"] != version {
		t.Fatalf("start = %v", st)
	}
	if has(logs[0], "check") == nil || has(logs[0], "progress") == nil {
		t.Fatal("the checks and the progress are missing")
	}
	if res := lastResult(t, logs[0]); res["ok"] != true || res["exit"] != float64(0) {
		t.Fatalf("result = %v", res)
	}
	// A removal never takes the log, and --silent still writes one.
	if code, _, e := r.run("uninstall", "--yes", "--silent", "demo"); code != exitOK {
		t.Fatalf("%d %s", code, e)
	}
	if got := readLogs(t); len(got) != 2 || got[1][0]["command"] != "uninstall" || has(got[1], "removed") == nil {
		t.Fatalf("after the removal: %d logs", len(got))
	}
}

func TestJSONResultCarriesTheLogPath(t *testing.T) {
	r := goodRig(t)
	code, out, e := r.run(r.args("--json")...)
	if code != exitOK {
		t.Fatalf("%d\n%s%s", code, out, e)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var res map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		t.Fatal(err)
	}
	if p, _ := res["log"].(string); !strings.HasPrefix(p, logDir(t)) {
		t.Fatalf("result = %v", res)
	}
}

func TestARollbackIsLoggedWithItsError(t *testing.T) {
	core := zipOf(t, map[string]string{"bin/app.exe": "binary"})
	model := bytes.Repeat([]byte("m"), 4000)
	r := newRig(t,
		map[string][]byte{"/core.zip": core, "/m.bin": bytes.Repeat([]byte("X"), 4000)},
		map[string][]byte{"/core.zip": core, "/m.bin": model})
	code, _, errOut := r.run(r.args("--silent")...)
	if code != exitFailedRolledBack || !strings.Contains(errOut, "Log: ") {
		t.Fatalf("%d %s", code, errOut)
	}
	logs := readLogs(t)
	res := lastResult(t, logs[0])
	if res["exit"] != float64(exitFailedRolledBack) || res["error"] == "" || res["ok"] != false {
		t.Fatalf("result = %v", res)
	}
}

func TestARemovalThatLeavesItemsIsLogged(t *testing.T) {
	r := goodRig(t)
	if code, _, e := r.run(r.args("--silent")...); code != exitOK {
		t.Fatal(e)
	}
	if err := os.WriteFile(filepath.Join(r.app, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := r.run("uninstall", "--yes", "--silent", "demo"); code != exitLeftovers {
		t.Fatal("want exit 5")
	}
	logs := readLogs(t)
	last := logs[len(logs)-1]
	if has(last, "not_removed") == nil || lastResult(t, last)["exit"] != float64(exitLeftovers) {
		t.Fatalf("log = %v", last)
	}
}

func TestRepairIsLoggedOnlyWhenItActs(t *testing.T) {
	r := installed(t)
	before := len(readLogs(t))
	if err := os.Remove(filepath.Join(r.app, "bin", "app.exe")); err != nil {
		t.Fatal(err)
	}
	r.run(r.repairArgs()...) // only shows the problems
	r.run(r.args("--dry-run")...)
	if len(readLogs(t)) != before {
		t.Fatal("a plan or a dry run left a log")
	}
	if code, _, e := r.run(r.repairArgs("--yes")...); code != exitOK {
		t.Fatal(e)
	}
	logs := readLogs(t)
	if len(logs) != before+1 || logs[len(logs)-1][0]["command"] != "repair" {
		t.Fatalf("want one repair log, got %d", len(logs))
	}
}

func TestOnlyTheLastLogsAreKept(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "demo", "record.json")
	var last string
	for i := 0; i < keepLogs+3; i++ {
		l := newRunLog(rec, "install", nil)
		l.add("start", nil)
		l.commit()
		last = l.Path()
		l.close()
		time.Sleep(2 * time.Millisecond)
	}
	ents, _ := os.ReadDir(filepath.Join(filepath.Dir(rec), "log"))
	if len(ents) != keepLogs {
		t.Fatalf("kept %d logs, want %d", len(ents), keepLogs)
	}
	if _, err := os.Stat(last); err != nil {
		t.Fatal("the newest log was deleted")
	}
}

func TestALogThatCannotBeWrittenDoesNotChangeTheOutcome(t *testing.T) {
	r := goodRig(t)
	// A file where the log folder should be.
	if err := os.MkdirAll(filepath.Dir(logDir(t)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logDir(t), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := r.run(r.args("--silent")...)
	if code != exitOK {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	if n := strings.Count(errOut, "could not write the log"); n != 1 {
		t.Fatalf("the failure must be reported once, got %d: %s", n, errOut)
	}
}
