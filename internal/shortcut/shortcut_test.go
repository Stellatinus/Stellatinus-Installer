package shortcut

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func env(goos string, vars map[string]string) Env {
	return Env{GOOS: goos, Getenv: func(k string) string { return vars[k] }, ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist }}
}

func TestDirPerSystem(t *testing.T) {
	win := env("windows", map[string]string{"APPDATA": `C:\U\AppData\Roaming`, "ProgramData": `C:\PD`, "USERPROFILE": `C:\U`, "PUBLIC": `C:\Public`})
	lin := env("linux", map[string]string{"HOME": "/home/u"})
	mac := env("darwin", map[string]string{"HOME": "/Users/u"})
	for _, c := range []struct {
		e     Env
		p     Place
		scope string
		want  string
	}{
		{win, StartMenu, "user", filepath.Join(`C:\U\AppData\Roaming`, "Microsoft", "Windows", "Start Menu", "Programs")},
		{win, StartMenu, "machine", filepath.Join(`C:\PD`, "Microsoft", "Windows", "Start Menu", "Programs")},
		{win, Desktop, "user", filepath.Join(`C:\U`, "Desktop")},
		{win, Desktop, "machine", filepath.Join(`C:\Public`, "Desktop")},
		{lin, StartMenu, "user", "/home/u/.local/share/applications"},
		{lin, StartMenu, "machine", "/usr/share/applications"},
		{lin, Desktop, "user", "/home/u/Desktop"},
		{mac, StartMenu, "user", "/Users/u/Applications"},
		{mac, StartMenu, "machine", "/Applications"},
		{mac, Desktop, "user", "/Users/u/Desktop"},
	} {
		got, err := Dir(c.p, c.scope, c.e)
		if err != nil || got != c.want {
			t.Errorf("%s %s %s: got %q, %v; want %q", c.e.GOOS, c.p, c.scope, got, err, c.want)
		}
	}
	for _, c := range []struct {
		e     Env
		p     Place
		scope string
		want  error
	}{
		{lin, Desktop, "machine", ErrUnavailable},
		{mac, Desktop, "machine", ErrUnavailable},
		{win, Taskbar, "user", ErrTaskbar},
		{env("plan9", nil), StartMenu, "user", ErrUnavailable},
	} {
		if _, err := Dir(c.p, c.scope, c.e); !errors.Is(err, c.want) {
			t.Errorf("%s %s %s: got %v, want %v", c.e.GOOS, c.p, c.scope, err, c.want)
		}
	}
	if _, err := Dir(StartMenu, "user", env("windows", nil)); err == nil {
		t.Error("an unset APPDATA must be an error, not a path under the working folder")
	}
	if _, err := Dir(StartMenu, "everyone", lin); err == nil {
		t.Error("an unknown scope must be refused")
	}
}

func TestLinuxDesktopFollowsUserDirs(t *testing.T) {
	e := env("linux", map[string]string{"HOME": "/home/u"})
	e.ReadFile = func(string) ([]byte, error) {
		return []byte("XDG_DOCUMENTS_DIR=\"$HOME/Docs\"\nXDG_DESKTOP_DIR=\"$HOME/Scrivania\"\n"), nil
	}
	if got, _ := Dir(Desktop, "user", e); got != "/home/u/Scrivania" {
		t.Fatalf("got %q, want the folder user-dirs.dirs names", got)
	}
	// A value that leaves the home folder through a variable or a command is not used.
	e.ReadFile = func(string) ([]byte, error) { return []byte("XDG_DESKTOP_DIR=\"$HOME/$(rm -rf x)\"\n"), nil }
	if got, _ := Dir(Desktop, "user", e); got != "/home/u/Desktop" {
		t.Fatalf("got %q, want the default for a value with a substitution", got)
	}
}

func TestFileNames(t *testing.T) {
	s := Spec{ID: "haedus", Name: "Haedus", Target: "/x/Haedus.app"}
	for goos, want := range map[string]string{"windows": "Haedus.lnk", "linux": "haedus.desktop", "darwin": "Haedus.app"} {
		if got, err := FileName(s, goos); err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", goos, got, err, want)
		}
	}
	if got, _ := FileName(Spec{ID: "mira", Name: "Mira", Target: "/x/mira"}, "darwin"); got != "Mira" {
		t.Errorf("a program that is not a bundle keeps the app's name, got %q", got)
	}
	for _, name := range []string{"A/B", `A\B`, "CON", "Bad:Name"} {
		if _, err := FileName(Spec{ID: "x", Name: name}, "windows"); err == nil {
			t.Errorf("the name %q must be refused", name)
		}
	}
}

func spec(t *testing.T, p Place) Spec {
	t.Helper()
	return Spec{Place: p, ID: "haedus", Name: "Haedus", Description: "Speech to text", Target: filepath.Join(t.TempDir(), "Haedus.exe"), Scope: "user"}
}

func TestWriteNeverOverwrites(t *testing.T) {
	home := t.TempDir()
	e := env("linux", map[string]string{"HOME": home})
	s := spec(t, StartMenu)
	path, err := Path(s, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(s, e, path); err != nil {
		t.Fatal(err)
	}
	// The applications folder was made, and the file is executable and readable.
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("stat: %v %v", info, err)
	}
	// A second write, and a file that someone else made, are left untouched.
	if err := os.WriteFile(path, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(s, e, path); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("got %v, want an error that is fs.ErrExist", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "theirs" {
		t.Fatalf("an existing file was overwritten: %q", got)
	}
	// A dangling link is also "there".
	link := filepath.Join(filepath.Dir(path), "other.desktop")
	if err := os.Symlink(filepath.Join(home, "nowhere"), link); err != nil {
		t.Skip("symbolic links are not available here")
	}
	if err := Write(s, e, link); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("a dangling link: got %v, want fs.ErrExist", err)
	}
}

func TestWriteDesktopNeedsTheFolder(t *testing.T) {
	home := t.TempDir()
	e := env("linux", map[string]string{"HOME": home})
	s := spec(t, Desktop)
	path, err := Path(s, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(s, e, path); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable: the desktop folder is not made", err)
	}
	if _, err := os.Stat(filepath.Join(home, "Desktop")); err == nil {
		t.Fatal("the desktop folder was created")
	}
	if err := os.Mkdir(filepath.Join(home, "Desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(s, e, path); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopEntryQuoting(t *testing.T) {
	s := Spec{Name: `A "B"`, Description: `back\slash`, Target: `/opt/my app/$x/50%/a"b`, Dir: "/opt/my app"}
	got := string(desktopEntry(s))
	for _, want := range []string{
		"Name=A \"B\"\n",
		`Comment=back\\slash` + "\n",
		// quoted for Exec, then escaped as a string: each backslash is doubled.
		`Exec="/opt/my app/\\$x/50%%/a\\"b"` + "\n",
		"Path=/opt/my app\n",
		"Terminal=false\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != 8 {
		t.Errorf("a value must not add lines:\n%s", got)
	}
}

func TestWriteRefusesControlCharacters(t *testing.T) {
	home := t.TempDir()
	e := env("linux", map[string]string{"HOME": home})
	s := spec(t, StartMenu)
	s.Description = "one\nExec=/bin/evil"
	path, _ := Path(s, e)
	if err := Write(s, e, path); err == nil {
		t.Fatal("a newline in the description would add a key to the file")
	}
	if _, err := os.Lstat(path); err == nil {
		t.Fatal("a file was written")
	}
	s = spec(t, StartMenu)
	s.Target = "relative/path"
	if err := Write(s, e, path); err == nil {
		t.Fatal("a relative target must be refused")
	}
}

func TestMacLinkPointsAtTheProgram(t *testing.T) {
	home := t.TempDir()
	e := env("darwin", map[string]string{"HOME": home})
	s := spec(t, StartMenu)
	s.Target = filepath.Join(t.TempDir(), "Haedus.app")
	path, _ := Path(s, e)
	if err := Write(s, e, path); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(path); err != nil || got != s.Target {
		t.Fatalf("readlink: %q, %v", got, err)
	}
}

// link is what the test reads back from a .lnk.
type link struct {
	flags         uint32
	base, baseUni string
	name, workDir string
}

// parseLnk reads the pieces of a shell link that lnk writes, following the
// offsets of MS-SHLLINK rather than the layout the writer happens to use.
func parseLnk(t *testing.T, b []byte) link {
	t.Helper()
	le := binary.LittleEndian
	if len(b) < 76 || le.Uint32(b) != 0x4C {
		t.Fatal("not a shell link header")
	}
	if !bytes.Equal(b[4:20], []byte{0x01, 0x14, 0x02, 0, 0, 0, 0, 0, 0xC0, 0, 0, 0, 0, 0, 0, 0x46}) {
		t.Fatal("wrong link CLSID")
	}
	l := link{flags: le.Uint32(b[20:])}
	pos := 76
	if l.flags&lnkHasLinkInfo == 0 {
		t.Fatal("no link info")
	}
	li := b[pos:]
	size := int(le.Uint32(li))
	if size > len(li) || le.Uint32(li[4:]) < 0x24 {
		t.Fatalf("link info size %d of %d", size, len(li))
	}
	cstr := func(off uint32) string {
		end := bytes.IndexByte(li[off:size], 0)
		if end < 0 {
			t.Fatal("unterminated string")
		}
		return string(li[off : int(off)+end])
	}
	l.base = cstr(le.Uint32(li[16:]))
	ustr := func(off uint32) string {
		var u []uint16
		for i := int(off); i+1 < size; i += 2 {
			c := le.Uint16(li[i:])
			if c == 0 {
				return string(utf16.Decode(u))
			}
			u = append(u, c)
		}
		t.Fatal("unterminated Unicode string")
		return ""
	}
	l.baseUni = ustr(le.Uint32(li[28:]))
	pos += size
	str := func() string {
		n := int(le.Uint16(b[pos:]))
		pos += 2
		u := make([]uint16, n)
		for i := range u {
			u[i] = le.Uint16(b[pos:])
			pos += 2
		}
		return string(utf16.Decode(u))
	}
	if l.flags&lnkHasName != 0 {
		l.name = str()
	}
	if l.flags&lnkHasWorkDir != 0 {
		l.workDir = str()
	}
	if len(b)-pos != 4 || le.Uint32(b[pos:]) != 0 {
		t.Fatalf("expected the 4-byte terminal block at the end, %d bytes left", len(b)-pos)
	}
	return l
}

func TestLnkReadsBack(t *testing.T) {
	s := Spec{Name: "Haedus", Description: "Trascrizione vocale — è locale", Target: `C:\Users\Zoë\AppData\Local\Haedus\Haedus.exe`, Dir: `C:\Users\Zoë\AppData\Local\Haedus`}
	l := parseLnk(t, lnk(s))
	if l.baseUni != s.Target {
		t.Errorf("Unicode path %q, want %q", l.baseUni, s.Target)
	}
	if l.base != `C:\Users\Zo?\AppData\Local\Haedus\Haedus.exe` {
		t.Errorf("ANSI path %q", l.base)
	}
	if l.name != s.Description || l.workDir != s.Dir {
		t.Errorf("name %q, work dir %q", l.name, l.workDir)
	}
	if l.flags&lnkIsUnicode == 0 {
		t.Error("the string data must be flagged Unicode")
	}
	// Without a description there is no name field.
	s.Description = ""
	if l := parseLnk(t, lnk(s)); l.name != "" || l.workDir != s.Dir {
		t.Errorf("without a description: %+v", l)
	}
}

func TestWriteWindowsFile(t *testing.T) {
	appdata := t.TempDir()
	e := env("windows", map[string]string{"APPDATA": appdata})
	s := spec(t, StartMenu)
	path, err := Path(s, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(s, e, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if l := parseLnk(t, data); l.baseUni != s.Target {
		t.Fatalf("the written file points at %q, want %q", l.baseUni, s.Target)
	}
}
