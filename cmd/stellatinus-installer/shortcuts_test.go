package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withShortcuts makes the manifest offer every shortcut and name a program that
// the rig's zip holds, for every system, and gives the test a home with a
// desktop folder. It returns the home, where the shortcuts will go.
func (r *rig) withShortcuts() string {
	r.t.Helper()
	b, err := os.ReadFile(r.manifest)
	if err != nil {
		r.t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		r.t.Fatal(err)
	}
	doc["shortcuts"] = map[string]any{
		"start_menu": map[string]any{"offer": true},
		"desktop":    map[string]any{"offer": true},
		"taskbar":    map[string]any{"offer": true},
	}
	prog := "bin/app.exe"
	doc["launch"] = map[string]any{"location": "install", "path": map[string]any{"windows": prog, "linux": prog, "darwin": prog}}
	b, _ = json.Marshal(doc)
	if err := os.WriteFile(r.manifest, b, 0o600); err != nil {
		r.t.Fatal(err)
	}
	home := os.Getenv("HOME")
	r.t.Setenv("USERPROFILE", home)
	r.t.Setenv("XDG_DATA_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, "Desktop"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	return home
}

// shortcutFiles lists what looks like the demo's shortcut under home.
func shortcutFiles(t *testing.T, home string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() || d != nil && d.Type()&fs.ModeSymlink != 0 {
			switch strings.ToLower(filepath.Base(p)) {
			case "demo.lnk", "demo.desktop", "demo":
				out = append(out, p)
			}
		}
		return nil
	})
	return out
}

func TestShortcutsAreCreatedAndRemovedFromTheCommandLine(t *testing.T) {
	r := goodRig(t)
	home := r.withShortcuts()
	code, out, errOut := r.run(r.args("--shortcut", "start_menu,desktop")...)
	if code != exitOK {
		t.Fatalf("install exit %d\n%s%s", code, out, errOut)
	}
	if got := shortcutFiles(t, home); len(got) != 2 {
		t.Fatalf("want 2 shortcuts, found %v\n%s", got, out)
	}
	if strings.Count(out, "Shortcut: ") != 2 {
		t.Fatalf("the output must name both shortcuts:\n%s", out)
	}
	code, out, errOut = r.run("uninstall", "--yes", "demo")
	if code != exitOK {
		t.Fatalf("uninstall exit %d\n%s%s", code, out, errOut)
	}
	if got := shortcutFiles(t, home); len(got) != 0 {
		t.Fatalf("shortcuts left behind: %v", got)
	}
}

func TestShortcutsInTheResponseFileAndTheTaskbarHint(t *testing.T) {
	r := goodRig(t)
	home := r.withShortcuts()
	resp := filepath.Join(t.TempDir(), "answers.json")
	if err := os.WriteFile(resp, []byte(`{"shortcuts": ["desktop", "taskbar"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := r.run(r.args("--response", resp, "--shortcut", "desktop")...)
	if code != exitOK {
		t.Fatalf("install exit %d\n%s%s", code, out, errOut)
	}
	// "desktop" is named twice and made once; the taskbar is only a note.
	if got := shortcutFiles(t, home); len(got) != 1 || !strings.Contains(got[0], "Desktop") {
		t.Fatalf("want one desktop shortcut, found %v", got)
	}
	if !strings.Contains(out, "Note: ") || !strings.Contains(out, "taskbar") {
		t.Fatalf("the taskbar hint is missing:\n%s", out)
	}
}

func TestDryRunShowsTheShortcutsAndMakesNone(t *testing.T) {
	r := goodRig(t)
	home := r.withShortcuts()
	code, out, errOut := r.run(r.args("--dry-run", "--shortcut", "start_menu,taskbar")...)
	if code != exitOK || !strings.Contains(out, "would make a start_menu shortcut at "+home) || !strings.Contains(out, "would not pin to the taskbar") {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	if got := shortcutFiles(t, home); len(got) != 0 {
		t.Fatalf("a dry run made %v", got)
	}
}

func TestShortcutJSONResultListsThem(t *testing.T) {
	r := goodRig(t)
	home := r.withShortcuts()
	code, out, errOut := r.run(r.args("--json", "--shortcut", "desktop")...)
	if code != exitOK {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var last struct {
		Event     string   `json:"event"`
		Shortcuts []string `json:"shortcuts"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil || last.Event != "result" || len(last.Shortcuts) != 1 || !strings.HasPrefix(last.Shortcuts[0], home) {
		t.Fatalf("the last line must be the result with the shortcut: %v %+v\n%s", err, last, lines[len(lines)-1])
	}
}

func TestBadShortcutRequestsStopBeforeAnything(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		code int
	}{
		"unknown place": {[]string{"--shortcut", "dock"}, exitUsage},
		"empty place":   {[]string{"--shortcut", ""}, exitUsage},
	} {
		t.Run(name, func(t *testing.T) {
			r := goodRig(t)
			r.withShortcuts()
			before := tree(t, r.root)
			code, _, errOut := r.run(r.args(tc.args...)...)
			if code != tc.code || !strings.Contains(errOut, "not a place for a shortcut") {
				t.Fatalf("%d %s", code, errOut)
			}
			if !same(tree(t, r.root), before) || r.hits.Load() != 0 {
				t.Fatal("something was done")
			}
		})
	}

	// A manifest that does not offer the place refuses it with exit 1.
	r := goodRig(t) // offers no shortcuts at all
	code, _, errOut := r.run(r.args("--shortcut", "desktop")...)
	if code != exitProblem || !strings.Contains(errOut, "does not offer") {
		t.Fatalf("%d %s", code, errOut)
	}
	if r.hits.Load() != 0 {
		t.Fatal("something was downloaded")
	}
}
