package opener

import (
	"errors"
	"strings"
	"testing"
)

func env(goos string, have ...string) Environment {
	has := map[string]bool{}
	for _, h := range have {
		has[h] = true
	}
	return Environment{
		GOOS: goos,
		LookPath: func(n string) (string, error) {
			if has[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Exists: func(p string) bool { return has[p] },
		Getenv: func(k string) string {
			return map[string]string{"ProgramFiles": `C:\Program Files`, "ProgramFiles(x86)": `C:\Program Files (x86)`, "LocalAppData": `C:\Users\u\AppData\Local`}[k]
		},
		ProfileDir: "/tmp/profile",
	}
}

const addr = "http://127.0.0.1:5555/?t=abc"

func TestWindowsPrefersEdgeInAppMode(t *testing.T) {
	e := env("windows")
	e.Exists = func(p string) bool { return strings.HasSuffix(p, "msedge.exe") }
	p, err := Choose(e, addr, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindApp || !strings.HasSuffix(p.Program, "msedge.exe") {
		t.Fatalf("plan: %+v", p)
	}
	joined := strings.Join(p.Args, " ")
	for _, want := range []string{"--app=" + addr, "--window-size=980,720", "--user-data-dir=/tmp/profile", "--no-first-run"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q do not contain %q", joined, want)
		}
	}
}

func TestWindowsFallsBackToChromeThenTheDefaultBrowser(t *testing.T) {
	e := env("windows")
	e.Exists = func(p string) bool { return strings.HasSuffix(p, "chrome.exe") }
	p, err := Choose(e, addr, "auto")
	if err != nil || p.Kind != KindApp || !strings.HasSuffix(p.Program, "chrome.exe") {
		t.Fatalf("chrome: %+v %v", p, err)
	}
	e.Exists = func(string) bool { return false }
	p, err = Choose(e, addr, "auto")
	if err != nil || p.Kind != KindDefault || p.Program != "rundll32" || p.Args[1] != addr {
		t.Fatalf("default browser on Windows: %+v %v", p, err)
	}
}

func TestMacOSUsesAChromiumAppWindowOrOpen(t *testing.T) {
	e := env("darwin", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	p, err := Choose(e, addr, "auto")
	if err != nil || p.Kind != KindApp || !strings.Contains(p.Program, "Google Chrome") {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = Choose(env("darwin"), addr, "auto")
	if err != nil || p.Kind != KindDefault || p.Program != "open" || p.Args[0] != addr {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestLinuxLooksForChromiumFamilyThenXdgOpen(t *testing.T) {
	p, err := Choose(env("linux", "chromium", "xdg-open"), addr, "auto")
	if err != nil || p.Kind != KindApp || !strings.HasSuffix(p.Program, "/chromium") {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = Choose(env("linux", "xdg-open"), addr, "auto")
	if err != nil || p.Kind != KindDefault || !strings.HasSuffix(p.Program, "xdg-open") {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err = Choose(env("linux"), addr, "auto"); err == nil || strings.Contains(err.Error(), "t=abc") {
		t.Fatalf("with no browser at all the error must say so and must not print the one-time token: %v", err)
	}
}

func TestModes(t *testing.T) {
	e := env("linux", "chromium", "xdg-open")
	if p, _ := Choose(e, addr, "default"); p.Kind != KindDefault {
		t.Errorf("mode default gave %+v", p)
	}
	if _, err := Choose(env("linux", "xdg-open"), addr, "app"); err == nil {
		t.Error("mode app with no Chromium must fail instead of quietly opening a tab")
	}
	if _, err := Choose(e, addr, "fullscreen"); err == nil {
		t.Error("an unknown mode was accepted")
	}
	if p, err := Choose(e, addr, ""); err != nil || p.Kind != KindApp {
		t.Errorf("an empty mode means auto: %+v %v", p, err)
	}
}

func TestNoProfileMeansNoProfileFlag(t *testing.T) {
	e := env("linux", "chromium")
	e.ProfileDir = ""
	p, _ := Choose(e, addr, "auto")
	if strings.Contains(strings.Join(p.Args, " "), "--user-data-dir") {
		t.Fatalf("an empty ProfileDir must not add the flag: %v", p.Args)
	}
}
