package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/pack"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

// cmdInstallEmbedded is what a packed installer does when it is started with no
// arguments: it says what it would install and, if the person agrees, installs
// with the default folders. There is no window yet, so this is the whole
// interaction; anything else is a command with flags.
func cmdInstallEmbedded(ctx context.Context, d deps, stdout, stderr io.Writer) int {
	data, err := embeddedManifest(d)
	if err != nil {
		fprintln(stderr, err)
		return exitProblem
	}
	m, err := manifest.Parse(bytes.NewReader(data))
	if err != nil {
		fprintln(stderr, err)
		return exitProblem
	}
	in := d.stdin
	if in == nil {
		in = os.Stdin
	}
	rd := bufio.NewReader(in)
	fprintf(stdout, "%s %s\n", m.App.Name, m.App.Version)
	if m.App.Description != "" {
		fprintf(stdout, "%s\n", m.App.Description)
	}
	// Say what "the defaults" are before asking, so the answer means something.
	for _, l := range m.Locations {
		if p, err := l.DefaultPath(runtime.GOOS, env); err == nil {
			fprintf(stdout, "  %s: %s\n", m.Text("", "location."+l.ID+".label"), p)
		}
	}
	args := defaultShortcutArgs(m)
	if len(args) > 0 {
		fprintf(stdout, "  Shortcuts: %s\n", args[1])
	}
	fprint(stdout, "Install it with the default folders? [y/N] ")
	line, _ := rd.ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		fprintln(stdout, "Nothing was installed.")
		return exitOK
	}
	code := cmdInstall(ctx, d, args, stdout, stderr)
	// Started by a double click, the console closes with the program; wait, so
	// the result can be read. Without a terminal this returns at once.
	fprint(stdout, "Press Enter to close. ")
	_, _ = rd.ReadString('\n')
	return code
}

// defaultShortcutArgs is the --shortcut option for the shortcuts the manifest
// ticks by default (the taskbar is never made, so it is left out), or nil.
func defaultShortcutArgs(m *manifest.Manifest) []string {
	var places []string
	if m.Shortcuts.StartMenu.Offer && m.Shortcuts.StartMenu.Default {
		places = append(places, "start_menu")
	}
	if m.Shortcuts.Desktop.Offer && m.Shortcuts.Desktop.Default {
		places = append(places, "desktop")
	}
	if len(places) == 0 {
		return nil
	}
	return []string{"--shortcut", strings.Join(places, ",")}
}

// cmdPack appends a manifest to an engine and writes the single-file installer.
func cmdPack(d deps, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	engine := fs.String("engine", "", "the engine to pack into (default: this program)")
	manPath := fs.String("manifest", "", "the manifest to append (required)")
	out := fs.String("out", "", "the installer to write (required)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *manPath == "" || *out == "" {
		fprint(stderr, usage)
		return exitUsage
	}
	// The manifest is validated here so a broken one never ships.
	m, err := manifest.Load(*manPath)
	if err != nil {
		fprintln(stderr, err)
		return exitProblem
	}
	data, err := os.ReadFile(*manPath)
	if err != nil {
		fprintln(stderr, err)
		return exitProblem
	}
	eng := *engine
	if eng == "" {
		if eng = d.exe; eng == "" {
			if eng, err = os.Executable(); err != nil {
				fprintln(stderr, err)
				return exitProblem
			}
		}
	}
	if err := pack.Pack(eng, data, *out); err != nil {
		fprintln(stderr, err)
		return exitProblem
	}
	fprintf(stdout, "Wrote %s: %s %s\n", *out, m.App.Name, m.App.Version)
	return exitOK
}

// cmdRegister is how an app tells the installer about files it made after the
// installation (register), or that it removed them itself (unregister).
func cmdRegister(args []string, stdout, stderr io.Writer, add bool) int {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "user", "user or machine, to find the record of an app id")
	loc := fs.String("location", "", "the location the paths are in (required to register)")
	origin := fs.String("origin", "app", "what made these files, shown in the removal screen")
	recursive := fs.Bool("recursive", false, "also register what is inside a folder")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() < 2 || (add && *loc == "") {
		fprint(stderr, usage)
		return exitUsage
	}
	target := fs.Arg(0)
	recPath := target
	if _, err := os.Stat(target); err != nil && !strings.ContainsAny(target, `/\`) && !strings.HasSuffix(target, ".json") {
		p, perr := record.DefaultPath(target, *scope)
		if perr != nil {
			fprintln(stderr, perr)
			return exitProblem
		}
		recPath = p
	}
	r, err := record.Load(recPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fprintf(stderr, "no installation record at %s\n", recPath)
		} else {
			fprintln(stderr, err)
		}
		return exitProblem
	}
	paths := make([]string, 0, fs.NArg()-1)
	for _, p := range fs.Args()[1:] {
		abs, err := filepath.Abs(p)
		if err != nil {
			fprintln(stderr, err)
			return exitUsage
		}
		paths = append(paths, abs)
	}
	n := 0
	if add {
		if n, err = r.Register(*loc, *origin, *recursive, paths...); err != nil {
			fprintln(stderr, err)
			return exitProblem
		}
	} else {
		for _, p := range paths {
			if o, ok := r.Owner(p); ok && o == record.OwnerInstaller && r.Remove(p) {
				n++
			}
		}
	}
	if err := r.Save(recPath); err != nil {
		fprintln(stderr, err)
		return exitProblem
	}
	verb := "Registered"
	if !add {
		verb = "Unregistered"
	}
	fprintf(stdout, "%s %d entr%s.\n", verb, n, map[bool]string{true: "y", false: "ies"}[n == 1])
	return exitOK
}
