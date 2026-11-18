package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
	"github.com/Stellatinus/Stellatinus-Installer/internal/shortcut"
)

func offered(m *manifest.Manifest, p shortcut.Place) bool {
	switch p {
	case shortcut.StartMenu:
		return m.Shortcuts.StartMenu.Offer
	case shortcut.Desktop:
		return m.Shortcuts.Desktop.Offer
	case shortcut.Taskbar:
		return m.Shortcuts.Taskbar.Offer
	}
	return false
}

// checkShortcuts refuses, before anything is written, a request the manifest
// does not allow: an unknown place, a place it does not offer, or a program it
// does not name for this system.
func checkShortcuts(m *manifest.Manifest, opt Options) error {
	return CheckShortcuts(m, opt.Shortcuts, opt.GOOS)
}

// CheckShortcuts is the check Run does first, for a caller that wants to
// refuse a request before showing anything else.
func CheckShortcuts(m *manifest.Manifest, places []shortcut.Place, goos string) error {
	seen := map[shortcut.Place]bool{}
	for _, p := range places {
		if !p.Valid() {
			return fmt.Errorf("%q is not a place for a shortcut (start_menu, desktop or taskbar)", p)
		}
		if seen[p] {
			return fmt.Errorf("the shortcut %q is asked for twice", p)
		}
		seen[p] = true
		if !offered(m, p) {
			return fmt.Errorf("%s does not offer a %s shortcut", m.App.Name, p)
		}
		if p == shortcut.Taskbar {
			continue
		}
		if _, _, ok := m.LaunchPath(goos); !ok {
			return fmt.Errorf("%s names no program to start on %s, so it has no shortcut there", m.App.Name, goos)
		}
	}
	return nil
}

// ShortcutSpec is what a shortcut for a place would be, for a caller that
// wants to show the path in advance (the dry run).
func ShortcutSpec(m *manifest.Manifest, p shortcut.Place, scope, goos string, paths map[string]string) (shortcut.Spec, error) {
	loc, rel, ok := m.LaunchPath(goos)
	if !ok {
		return shortcut.Spec{}, fmt.Errorf("%s names no program to start on %s", m.App.Name, goos)
	}
	root, ok := paths[loc]
	if !ok {
		return shortcut.Spec{}, fmt.Errorf("no folder for %q", loc)
	}
	target := filepath.Join(root, rel)
	return shortcut.Spec{Place: p, ID: m.App.ID, Name: m.App.Name, Description: m.App.Description, Target: target, Dir: filepath.Dir(target), Scope: scope}, nil
}

func (r *runner) note(format string, a ...any) {
	n := fmt.Sprintf(format, a...)
	r.notes = append(r.notes, n)
	r.logf("%s", n)
}

// makeShortcuts creates the shortcuts that were asked for. Each is recorded
// before it is written, so an interruption cannot leave one behind that
// uninstall does not know. A shortcut that cannot be made for a reason that is
// not the user's doing (it is already there, the desktop folder is missing, the
// program was not unpacked) is reported and skipped; it never fails the
// installation and never overwrites anything.
func (r *runner) makeShortcuts(scope string) error {
	env := shortcut.Env{GOOS: r.opt.GOOS, Getenv: r.opt.Env}
	for _, place := range r.opt.Shortcuts {
		if place == shortcut.Taskbar {
			r.note("%v", shortcut.ErrTaskbar)
			continue
		}
		spec, err := ShortcutSpec(r.m, place, scope, r.opt.GOOS, r.paths)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(spec.Target); err != nil {
			r.note("no %s shortcut: the program %s was not found", place, spec.Target)
			continue
		}
		path, err := shortcut.Path(spec, env)
		if err != nil {
			if errors.Is(err, shortcut.ErrUnavailable) {
				r.note("no %s shortcut: %v", place, err)
				continue
			}
			return fmt.Errorf("the %s shortcut: %w", place, err)
		}
		if _, err := os.Lstat(path); err == nil {
			r.note("no %s shortcut: %s is already there and is not touched", place, path)
			continue
		}
		e := record.Entry{Path: path, Kind: record.KindShortcut, Owner: record.OwnerInstaller, Origin: string(place)}
		if err := r.track(e); err != nil {
			return err
		}
		if err := r.save(); err != nil {
			return err
		}
		if err := shortcut.Write(spec, env, path); err != nil {
			// Whatever the reason, nothing of ours is at path: forget it, and do
			// not delete a file someone else put there in the meantime.
			r.untrack(path)
			if errors.Is(err, fs.ErrExist) || errors.Is(err, shortcut.ErrUnavailable) {
				r.note("no %s shortcut: %v", place, err)
				continue
			}
			return fmt.Errorf("the %s shortcut: %w", place, err)
		}
		r.shortcuts = append(r.shortcuts, path)
		r.logf("wrote the %s shortcut %s", place, path)
	}
	return nil
}
