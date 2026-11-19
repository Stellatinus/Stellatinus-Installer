package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
	"github.com/Stellatinus/Stellatinus-Installer/internal/shortcut"
)

func withLaunch(doc map[string]any) {
	doc["shortcuts"] = map[string]any{
		"start_menu": map[string]any{"offer": true},
		"desktop":    map[string]any{"offer": true},
		"taskbar":    map[string]any{"offer": true},
	}
	doc["launch"] = map[string]any{"location": "install", "path": map[string]any{"windows": "bin/Demo.exe", "linux": "bin/demo"}}
}

// shortcutZip holds a program under bin/ for both systems.
func shortcutZip(t *testing.T) []byte {
	return makeZip(t, zent{name: "bin/demo", data: []byte("#!/bin/sh\n"), mode: 0o755}, zent{name: "bin/Demo.exe", data: []byte("MZ")})
}

type shortcutEnv struct {
	env
	home string
	vars map[string]string
}

func newShortcutEnv(t *testing.T, goos string) shortcutEnv {
	e := newEnv(t)
	home := filepath.Join(e.root, "home")
	if err := os.MkdirAll(filepath.Join(home, "Desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"HOME": home, "USERPROFILE": home, "APPDATA": filepath.Join(home, "AppData")}
	return shortcutEnv{env: e, home: home, vars: vars}
}

func runShortcuts(t *testing.T, goos string, places ...shortcut.Place) (*Result, shortcutEnv, []string, error) {
	t.Helper()
	se := newShortcutEnv(t, goos)
	before := snapshot(t, se.root)
	data := shortcutZip(t)
	srv := tlsServer(t, serveBytes(data))
	m := buildX(t, withLaunch, comp{id: "app", kind: "zip", location: "install", url: srv.URL + "/app.zip", data: data})
	opt := se.opts(srv)
	opt.GOOS = goos
	opt.Shortcuts = places
	opt.Env = func(k string) string { return se.vars[k] }
	res, err := Run(context.Background(), m, opt)
	return res, se, before, err
}

func TestShortcutsAreCreatedRecordedAndRemovedWithTheRest(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			res, se, _, err := runShortcuts(t, goos, shortcut.StartMenu, shortcut.Desktop)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Shortcuts) != 2 || len(res.ShortcutNotes) != 0 {
				t.Fatalf("shortcuts %v, notes %v", res.Shortcuts, res.ShortcutNotes)
			}
			ext := map[string]string{"linux": ".desktop", "windows": ".lnk"}[goos]
			for _, p := range res.Shortcuts {
				if !strings.HasSuffix(p, ext) || !strings.HasPrefix(p, se.home) {
					t.Errorf("unexpected shortcut path %s", p)
				}
				data, rerr := os.ReadFile(p)
				if rerr != nil {
					t.Fatal(rerr)
				}
				prog := map[string]string{"linux": "bin/demo", "windows": "bin/Demo.exe"}[goos]
				want := filepath.Join(se.paths["install"], filepath.FromSlash(prog))
				// A backslash (a Windows host) is escaped twice in the file: once for
				// the Exec quoting and once as a string.
				quoted := strings.ReplaceAll(want, `\`, `\\\\`)
				if goos == "linux" && !bytes.Contains(data, []byte(`Exec="`+quoted+`"`)) {
					t.Errorf("the shortcut does not start %s:\n%s", want, data)
				}
				if o, ok := res.Record.Owner(p); !ok || o != record.OwnerInstaller {
					t.Errorf("%s must be recorded as the installer's: %v %v", p, o, ok)
				}
			}
			// Removal takes the shortcuts with the rest and leaves nothing behind.
			if rep := res.Record.Apply(res.Record.Plan(record.Choices{})); len(rep.Failed) != 0 {
				t.Fatalf("removal: %+v", rep.Failed)
			}
			for _, p := range res.Shortcuts {
				if _, err := os.Lstat(p); err == nil {
					t.Errorf("%s was left behind", p)
				}
			}
			for _, s := range snapshot(t, se.root) {
				if strings.Contains(s, "demo.desktop") || strings.Contains(s, "Demo.lnk") {
					t.Errorf("left behind: %s", s)
				}
			}
		})
	}
}

func TestADesktopThatIsNotAFolderIsANoteNotAFailure(t *testing.T) {
	se := newShortcutEnv(t, "linux")
	data := shortcutZip(t)
	srv := tlsServer(t, serveBytes(data))
	m := buildX(t, withLaunch, comp{id: "app", kind: "zip", location: "install", url: srv.URL + "/app.zip", data: data})
	opt := se.opts(srv)
	opt.GOOS = "linux"
	opt.Shortcuts = []shortcut.Place{shortcut.StartMenu, shortcut.Desktop}
	opt.Env = func(k string) string { return se.vars[k] }
	if err := os.RemoveAll(filepath.Join(se.home, "Desktop")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(se.home, "Desktop"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), m, opt)
	if err != nil {
		t.Fatalf("a missing desktop is a note, not a failure: %v", err)
	}
	if len(res.Shortcuts) != 1 || len(res.ShortcutNotes) != 1 || !strings.Contains(res.ShortcutNotes[0], "desktop") {
		t.Fatalf("shortcuts %v, notes %v", res.Shortcuts, res.ShortcutNotes)
	}
}

func TestAnExistingShortcutIsNeverOverwritten(t *testing.T) {
	se := newShortcutEnv(t, "linux")
	theirs := filepath.Join(se.home, "Desktop", "demo.desktop")
	if err := os.WriteFile(theirs, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := shortcutZip(t)
	srv := tlsServer(t, serveBytes(data))
	m := buildX(t, withLaunch, comp{id: "app", kind: "zip", location: "install", url: srv.URL + "/app.zip", data: data})
	opt := se.opts(srv)
	opt.GOOS = "linux"
	opt.Shortcuts = []shortcut.Place{shortcut.Desktop}
	opt.Env = func(k string) string { return se.vars[k] }
	res, err := Run(context.Background(), m, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shortcuts) != 0 || len(res.ShortcutNotes) != 1 || !strings.Contains(res.ShortcutNotes[0], "already there") {
		t.Fatalf("shortcuts %v, notes %v", res.Shortcuts, res.ShortcutNotes)
	}
	if _, ok := res.Record.Owner(theirs); ok {
		t.Fatal("a file that was already there must not be recorded as the installer's")
	}
	// Uninstall leaves it alone.
	if rep := res.Record.Apply(res.Record.Plan(record.Choices{})); len(rep.Failed) != 0 {
		t.Fatal(rep.Failed)
	}
	if got, _ := os.ReadFile(theirs); string(got) != "theirs" {
		t.Fatalf("the user's file was changed: %q", got)
	}
}

func TestAFailedInstallationTakesItsShortcutsAway(t *testing.T) {
	se := newShortcutEnv(t, "linux")
	data := shortcutZip(t)
	srv := tlsServer(t, serveBytes(data))
	// The desktop shortcut is made first; then the Start menu folder cannot be
	// made (a file stands where it goes), which fails the run.
	m := buildX(t, withLaunch, comp{id: "app", kind: "zip", location: "install", url: srv.URL + "/app.zip", data: data})
	opt := se.opts(srv)
	opt.GOOS = "linux"
	opt.Shortcuts = []shortcut.Place{shortcut.Desktop, shortcut.StartMenu}
	opt.Env = func(k string) string { return se.vars[k] }
	if err := os.MkdirAll(filepath.Join(se.home, ".local", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(se.home, ".local", "share", "applications"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, se.root)
	_, err := Run(context.Background(), m, opt)
	if err == nil {
		t.Fatal("a Start menu folder that cannot be made must fail the run")
	}
	mustRolledBack(t, err)
	if _, serr := os.Lstat(filepath.Join(se.home, "Desktop", "demo.desktop")); serr == nil {
		t.Fatal("the desktop shortcut made before the failure was left behind")
	}
	after := snapshot(t, se.root)
	if !equal(after, before) {
		t.Fatalf("not rolled back:\nbefore %v\nafter  %v", before, after)
	}
}

func TestTheTaskbarIsAHintNotAShortcut(t *testing.T) {
	res, se, _, err := runShortcuts(t, "windows", shortcut.Taskbar)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shortcuts) != 0 || len(res.ShortcutNotes) != 1 || !strings.Contains(res.ShortcutNotes[0], "taskbar") {
		t.Fatalf("shortcuts %v, notes %v", res.Shortcuts, res.ShortcutNotes)
	}
	for _, s := range snapshot(t, se.home) {
		if strings.Contains(s, "lnk") {
			t.Fatalf("something was written for the taskbar: %s", s)
		}
	}
}

func TestShortcutRequestsTheManifestDoesNotAllowAreRefusedFirst(t *testing.T) {
	for name, tc := range map[string]struct {
		places []shortcut.Place
		goos   string
		mutate func(map[string]any)
	}{
		"unknown place": {[]shortcut.Place{"dock"}, "linux", withLaunch},
		"twice":         {[]shortcut.Place{shortcut.Desktop, shortcut.Desktop}, "linux", withLaunch},
		"not offered": {[]shortcut.Place{shortcut.Desktop}, "linux", func(d map[string]any) {
			withLaunch(d)
			d["shortcuts"] = map[string]any{"start_menu": map[string]any{"offer": true}}
		}},
		"no program on system": {[]shortcut.Place{shortcut.Desktop}, "darwin", withLaunch},
	} {
		t.Run(name, func(t *testing.T) {
			se := newShortcutEnv(t, tc.goos)
			data := shortcutZip(t)
			srv := tlsServer(t, serveBytes(data))
			m := buildX(t, tc.mutate, comp{id: "app", kind: "zip", location: "install", url: srv.URL + "/app.zip", data: data})
			opt := se.opts(srv)
			opt.GOOS = tc.goos
			opt.Shortcuts = tc.places
			before := snapshot(t, se.root)
			_, err := Run(context.Background(), m, opt)
			if err == nil {
				t.Fatal("must be refused")
			}
			var ie *Error
			if errors.As(err, &ie) {
				t.Fatalf("must be refused before anything is done, not rolled back: %v", err)
			}
			if after := snapshot(t, se.root); !equal(after, before) {
				t.Fatalf("something was created: %v", after)
			}
		})
	}
}

func TestAMissingProgramIsANote(t *testing.T) {
	se := newShortcutEnv(t, "linux")
	data := makeZip(t, zent{name: "other.txt", data: []byte("x")})
	srv := tlsServer(t, serveBytes(data))
	m := buildX(t, withLaunch, comp{id: "app", kind: "zip", location: "install", url: srv.URL + "/app.zip", data: data})
	opt := se.opts(srv)
	opt.GOOS = "linux"
	opt.Shortcuts = []shortcut.Place{shortcut.StartMenu}
	opt.Env = func(k string) string { return se.vars[k] }
	res, err := Run(context.Background(), m, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shortcuts) != 0 || len(res.ShortcutNotes) != 1 || !strings.Contains(res.ShortcutNotes[0], "was not found") {
		t.Fatalf("shortcuts %v, notes %v", res.Shortcuts, res.ShortcutNotes)
	}
}
