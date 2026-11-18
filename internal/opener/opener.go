// Package opener decides how to show the setup window's address and runs it.
// The best result is a small window with no tabs or address bar, which
// Chromium-based browsers give in "app" mode (Microsoft Edge is on every
// supported Windows; Chrome, Chromium and Edge may be on macOS and Linux);
// failing that, the system's default browser opens a normal tab. The choice is
// a pure function of the system and of which programs exist, so it is tested
// without opening anything. It does not serve the interface and does not know
// what the interface does.
package opener

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Kind says what the plan will give.
type Kind string

// The kinds of result.
const (
	KindApp     Kind = "app"     // a window with no tabs or address bar
	KindDefault Kind = "default" // a tab in the default browser
)

// Plan is a program and its arguments.
type Plan struct {
	Kind    Kind
	Program string
	Args    []string
	// Detail says in words what was chosen, for the log.
	Detail string
}

// Environment is what Choose needs to know about the system.
type Environment struct {
	GOOS     string
	LookPath func(name string) (string, error)
	Exists   func(path string) bool
	Getenv   func(string) string
	// ProfileDir is a folder for the browser's own profile. A separate profile
	// makes the browser a process of its own, which ends when the window is
	// closed; without it an already running browser takes the request and
	// returns at once, and the installer could not tell when the window closed.
	ProfileDir string
}

// Width and Height are the size asked for the window.
const (
	Width  = 980
	Height = 720
)

func appArgs(url, profile string) []string {
	args := []string{
		"--app=" + url,
		fmt.Sprintf("--window-size=%d,%d", Width, Height),
		"--no-first-run",
		"--no-default-browser-check",
	}
	if profile != "" {
		args = append(args, "--user-data-dir="+profile)
	}
	return args
}

// Choose returns the best way to open url on this system. mode is "auto"
// (an app window if a suitable browser exists, else the default browser),
// "app" (an app window or an error) or "default".
func Choose(env Environment, url, mode string) (Plan, error) {
	if mode != "auto" && mode != "app" && mode != "default" && mode != "" {
		return Plan{}, fmt.Errorf("unknown mode %q", mode)
	}
	if mode != "default" {
		if prog := findChromium(env); prog != "" {
			return Plan{Kind: KindApp, Program: prog, Args: appArgs(url, env.ProfileDir),
				Detail: "an app window of " + filepath.Base(prog)}, nil
		}
		if mode == "app" {
			return Plan{}, fmt.Errorf("no Edge, Chrome or Chromium was found to show an app window")
		}
	}
	return defaultBrowser(env, url)
}

func findChromium(env Environment) string {
	var names []string
	var fixed []string
	switch env.GOOS {
	case "windows":
		pf, pf86, local := env.Getenv("ProgramFiles"), env.Getenv("ProgramFiles(x86)"), env.Getenv("LocalAppData")
		for _, base := range []string{pf86, pf, local} {
			if base == "" {
				continue
			}
			fixed = append(fixed,
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
		}
		names = []string{"msedge", "chrome"}
	case "darwin":
		fixed = []string{
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		names = []string{"microsoft-edge", "microsoft-edge-stable", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}
	}
	for _, f := range fixed {
		if env.Exists(f) {
			return f
		}
	}
	for _, n := range names {
		if p, err := env.LookPath(n); err == nil && p != "" {
			return p
		}
	}
	return ""
}

func defaultBrowser(env Environment, url string) (Plan, error) {
	switch env.GOOS {
	case "windows":
		return Plan{Kind: KindDefault, Program: "rundll32", Args: []string{"url.dll,FileProtocolHandler", url}, Detail: "the default browser"}, nil
	case "darwin":
		return Plan{Kind: KindDefault, Program: "open", Args: []string{url}, Detail: "the default browser"}, nil
	default:
		if p, err := env.LookPath("xdg-open"); err == nil && p != "" {
			return Plan{Kind: KindDefault, Program: p, Args: []string{url}, Detail: "the default browser"}, nil
		}
		return Plan{}, fmt.Errorf("no browser could be found: install one, or open %s yourself", strings.SplitN(url, "?", 2)[0])
	}
}

// Start runs the plan. For an app window the returned wait function blocks
// until the window is closed; for the default browser it returns at once,
// because the installer cannot know when a tab closes.
func Start(ctx context.Context, p Plan) (wait func() error, err error) {
	cmd := exec.CommandContext(ctx, p.Program, p.Args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", p.Program, err)
	}
	if p.Kind == KindApp {
		return cmd.Wait, nil
	}
	go func() { _ = cmd.Wait() }()
	return func() error { return nil }, nil
}
