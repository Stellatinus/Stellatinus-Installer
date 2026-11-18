// Package shortcut works out where a shortcut to the installed program goes on
// each system and writes it: a .lnk file for the Start menu or the desktop on
// Windows, a .desktop file on Linux, a link to the application on macOS. A
// shortcut is never written over a file that is already there. It does not
// record anything (the installer does, so removal can follow the record), does
// not pin to the taskbar (no supported way to do that from an installer is
// known) and does not decide which shortcuts the user wants.
package shortcut

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Stellatinus/Stellatinus-Installer/internal/safepath"
)

// Place is where a shortcut goes.
type Place string

// Places. The taskbar is listed so it can be named, but nothing is ever
// written for it: see ErrTaskbar.
const (
	StartMenu Place = "start_menu"
	Desktop   Place = "desktop"
	Taskbar   Place = "taskbar"
)

// Valid reports whether p is a place this package knows.
func (p Place) Valid() bool { return p == StartMenu || p == Desktop || p == Taskbar }

var (
	// ErrTaskbar is returned for the taskbar: Windows has no supported way to
	// pin from an installer and the other systems have none that is stable, so
	// the caller shows a hint instead.
	ErrTaskbar = errors.New("pinning to the taskbar is not done by the installer: pin the program by hand after it has started once")
	// ErrUnavailable is returned when a place does not exist for this system,
	// scope or user (no desktop folder, no all-users desktop on Linux).
	ErrUnavailable = errors.New("this place is not available here")
)

// Env is what path decisions depend on, so a test can ask for the paths of
// another system without being on it.
type Env struct {
	GOOS   string
	Getenv func(string) string
	// ReadFile reads the XDG user directories file; nil uses os.ReadFile.
	ReadFile func(string) ([]byte, error)
}

func (e Env) get(k string) string {
	if e.Getenv == nil {
		return os.Getenv(k)
	}
	return e.Getenv(k)
}

func (e Env) read(p string) ([]byte, error) {
	if e.ReadFile == nil {
		return os.ReadFile(p)
	}
	return e.ReadFile(p)
}

// Spec describes one shortcut.
type Spec struct {
	Place Place
	// ID is the app id; Name is what people see; Description is one sentence.
	ID, Name, Description string
	// Target is the absolute path of the program; Dir is the folder it runs in
	// (empty: the target's folder).
	Target string
	Dir    string
	// Scope is "user" or "machine".
	Scope string
}

// Dir returns the folder a shortcut for the place goes in.
func Dir(p Place, scope string, env Env) (string, error) {
	if p == Taskbar {
		return "", ErrTaskbar
	}
	if scope != "user" && scope != "machine" {
		return "", fmt.Errorf("unknown scope %q", scope)
	}
	need := func(names ...string) ([]string, error) {
		out := make([]string, len(names))
		for i, n := range names {
			if out[i] = env.get(n); out[i] == "" {
				return nil, fmt.Errorf("the variable %s is not set", n)
			}
		}
		return out, nil
	}
	user := scope == "user"
	switch env.GOOS {
	case "windows":
		switch {
		case p == StartMenu && user:
			v, err := need("APPDATA")
			if err != nil {
				return "", err
			}
			return filepath.Join(v[0], "Microsoft", "Windows", "Start Menu", "Programs"), nil
		case p == StartMenu:
			v, err := need("ProgramData")
			if err != nil {
				return "", err
			}
			return filepath.Join(v[0], "Microsoft", "Windows", "Start Menu", "Programs"), nil
		case p == Desktop && user:
			v, err := need("USERPROFILE")
			if err != nil {
				return "", err
			}
			return filepath.Join(v[0], "Desktop"), nil
		case p == Desktop:
			v, err := need("PUBLIC")
			if err != nil {
				return "", err
			}
			return filepath.Join(v[0], "Desktop"), nil
		}
	case "linux":
		switch {
		case p == StartMenu && user:
			if d := env.get("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
				return filepath.Join(d, "applications"), nil
			}
			v, err := need("HOME")
			if err != nil {
				return "", err
			}
			return filepath.Join(v[0], ".local", "share", "applications"), nil
		case p == StartMenu:
			return "/usr/share/applications", nil
		case p == Desktop && user:
			return linuxDesktop(env)
		case p == Desktop:
			return "", fmt.Errorf("%w: Linux has no desktop folder for all users", ErrUnavailable)
		}
	case "darwin":
		switch {
		case p == StartMenu && user:
			v, err := need("HOME")
			if err != nil {
				return "", err
			}
			return filepath.Join(v[0], "Applications"), nil
		case p == StartMenu:
			return "/Applications", nil
		case p == Desktop && user:
			v, err := need("HOME")
			if err != nil {
				return "", err
			}
			return filepath.Join(v[0], "Desktop"), nil
		case p == Desktop:
			return "", fmt.Errorf("%w: macOS has no desktop folder for all users", ErrUnavailable)
		}
	default:
		return "", fmt.Errorf("%w: shortcuts are not made on %s", ErrUnavailable, env.GOOS)
	}
	return "", fmt.Errorf("unknown place %q", p)
}

// linuxDesktop finds the user's desktop folder: XDG_DESKTOP_DIR from
// user-dirs.dirs when it is set to something inside the home folder, else
// ~/Desktop.
func linuxDesktop(env Env) (string, error) {
	home := env.get("HOME")
	if home == "" {
		return "", errors.New("the variable HOME is not set")
	}
	cfg := env.get("XDG_CONFIG_HOME")
	if cfg == "" || !filepath.IsAbs(cfg) {
		cfg = filepath.Join(home, ".config")
	}
	if data, err := env.read(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			val, ok := strings.CutPrefix(line, "XDG_DESKTOP_DIR=")
			if !ok {
				continue
			}
			val = strings.Trim(val, `"`)
			val = strings.Replace(val, "$HOME", home, 1)
			if filepath.IsAbs(val) && !strings.ContainsAny(val, "$`\x00") {
				return filepath.Clean(val), nil
			}
		}
	}
	return filepath.Join(home, "Desktop"), nil
}

// FileName returns the name of the shortcut file. Windows uses the app's name
// ("Haedus.lnk"), Linux the app id ("haedus.desktop"), and macOS the name of
// the program it points to ("Haedus.app") or the app's name.
func FileName(s Spec, goos string) (string, error) {
	var n string
	switch goos {
	case "windows":
		n = s.Name + ".lnk"
	case "linux":
		n = s.ID + ".desktop"
	case "darwin":
		n = s.Name
		if strings.HasSuffix(strings.ToLower(s.Target), ".app") {
			n += ".app"
		}
	default:
		return "", fmt.Errorf("%w: shortcuts are not made on %s", ErrUnavailable, goos)
	}
	if err := safepath.ValidName(n); err != nil {
		return "", fmt.Errorf("the shortcut's file name: %w", err)
	}
	return n, nil
}

// Path returns the full path of the shortcut for a spec.
func Path(s Spec, env Env) (string, error) {
	dir, err := Dir(s.Place, s.Scope, env)
	if err != nil {
		return "", err
	}
	name, err := FileName(s, env.GOOS)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Write creates the shortcut at path, which must come from Path. It never
// writes over anything: if path exists it returns an error that satisfies
// errors.Is(err, fs.ErrExist) and has touched nothing. A missing Start menu
// folder is made (it is shared, never recorded and never removed); a missing
// desktop folder is not made.
func Write(s Spec, env Env, path string) error {
	if !filepath.IsAbs(s.Target) {
		return fmt.Errorf("the program %q is not an absolute path", s.Target)
	}
	if s.Dir == "" {
		s.Dir = filepath.Dir(s.Target)
	}
	for _, v := range []string{s.Name, s.Description, s.Target, s.Dir} {
		if !utf8.ValidString(v) || strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("%q cannot be used in a shortcut", v)
		}
	}
	dir := filepath.Dir(path)
	if s.Place == StartMenu {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: the folder %s does not exist", ErrUnavailable, dir)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s: %w", path, fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	switch env.GOOS {
	case "windows":
		return writeFile(path, lnk(s), 0o644)
	case "linux":
		return writeFile(path, desktopEntry(s), 0o755)
	case "darwin":
		return os.Symlink(s.Target, path)
	}
	return fmt.Errorf("%w: shortcuts are not made on %s", ErrUnavailable, env.GOOS)
}

// writeFile creates the file with O_EXCL, so a file that appears between the
// check and the write is never overwritten, and removes a half-written file.
func writeFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return os.Chmod(path, perm) // the umask may have narrowed it
}
