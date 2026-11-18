// Command window is a spike, not the product: it shows the setup window's web
// interface from the Go engine, to find out on real machines how it looks and
// behaves. It serves a small page on 127.0.0.1 (one-time address, session
// cookie, Host and Origin checks), opens it in an app window of Edge, Chrome or
// Chromium when one exists or in the default browser, and lets the page run the
// engine's real checks. It installs nothing. See docs/window.md for what it is
// meant to answer and what is still untested.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/checks"
	"github.com/Stellatinus/Stellatinus-Installer/internal/localui"
	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/opener"
	"github.com/Stellatinus/Stellatinus-Installer/internal/theme"
)

//go:embed ui
var uiFiles embed.FS

func main() {
	manifestPath := flag.String("manifest", "examples/haedus.json", "the manifest to show")
	mode := flag.String("mode", "auto", "auto, app (an app window or fail), default (the default browser) or none (only print the address)")
	timeout := flag.Duration("timeout", 10*time.Minute, "stop after this long")
	flag.Parse()
	if err := run(*manifestPath, *mode, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "window spike:", err)
		os.Exit(1)
	}
}

func run(manifestPath, mode string, timeout time.Duration) error {
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return err
	}
	quit := make(chan struct{}, 1)
	h, err := newHandler(m, func() {
		select {
		case quit <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return err
	}
	srv, err := localui.New(h)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	defer func() { _ = srv.Close() }()

	url, err := srv.NewURL()
	if err != nil {
		return err
	}
	fmt.Println("Serving", srv.String())
	if mode == "none" {
		fmt.Println("Open this address once, in a browser on this machine:", url)
		select {
		case <-quit:
		case <-ctx.Done():
		}
		return nil
	}

	profile, err := os.MkdirTemp("", "stellatinus-window-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(profile) }()
	plan, err := opener.Choose(opener.Environment{
		GOOS: runtime.GOOS, LookPath: exec.LookPath, Getenv: os.Getenv, ProfileDir: profile,
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
	}, url, mode)
	if err != nil {
		return err
	}
	fmt.Println("Opening", plan.Detail, "("+filepath.Base(plan.Program)+")")
	wait, err := opener.Start(ctx, plan)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case <-quit:
		fmt.Println("Closed from the page.")
	case err := <-done:
		if plan.Kind == opener.KindApp {
			fmt.Println("The window was closed.")
			return nil
		}
		_ = err
		// A tab in the default browser: nothing tells us when it closes, so wait
		// for the page's Close button or the timeout.
		select {
		case <-quit:
			fmt.Println("Closed from the page.")
		case <-ctx.Done():
		}
	case <-ctx.Done():
		fmt.Println("Stopped.")
	}
	return nil
}

// newHandler is what the page talks to. quit is called when the page asks to
// end the program.
func newHandler(m *manifest.Manifest, quit func()) (http.Handler, error) {
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, l := range m.Locations {
		p, err := l.DefaultPath(runtime.GOOS, os.Getenv)
		if err != nil {
			// A variable that is not set on this system is not a reason to stop a spike.
			p = "(no default on " + runtime.GOOS + ")"
		}
		paths[l.ID] = p
	}
	light, err := theme.Derive(m.App.Accent, false)
	if err != nil {
		return nil, err
	}
	dark, err := theme.Derive(m.App.Accent, true)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	mux.HandleFunc("GET /api/manifest", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": m.App.Name, "paths": paths, "palettes": map[string]any{
			"light": map[string]string{"fill": light.Fill, "ink": light.Ink, "text": light.Text, "soft": light.Soft},
			"dark":  map[string]string{"fill": dark.Fill, "ink": dark.Ink, "text": dark.Text, "soft": dark.Soft},
		}})
	})
	mux.HandleFunc("POST /api/checks", func(w http.ResponseWriter, r *http.Request) {
		usable := map[string]string{}
		for id, p := range paths {
			if filepath.IsAbs(p) {
				usable[id] = p
			}
		}
		writeJSON(w, checks.Run(m, usable, checks.Options{Scope: m.Scope, Elevated: checks.Elevated()}))
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
		go quit()
	})
	return mux, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
