package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Stellatinus/Stellatinus-Installer/internal/checks"
	"github.com/Stellatinus/Stellatinus-Installer/internal/install"
	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/pack"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
	"github.com/Stellatinus/Stellatinus-Installer/internal/shortcut"
)

// Exit codes. docs/cli.md lists them per command.
const (
	exitOK               = 0
	exitProblem          = 1 // invalid input, a check failed, nothing was changed
	exitUsage            = 2
	exitFailedRolledBack = 3 // the installation failed and everything it made was removed
	exitFailedPartial    = 4 // the installation failed and something could not be removed
	exitLeftovers        = 5 // the removal finished but some items could not be removed
)

// deps are the things a test replaces.
type deps struct {
	client *http.Client // nil: the installer's default client
	exe    string       // the installer's own file, to look for an appended manifest; empty: os.Executable
	stdin  io.Reader    // nil: os.Stdin
}

var errUsage = errors.New("usage")

// embeddedManifest returns the manifest appended to the installer's own file,
// or nil if it is the plain engine.
func embeddedManifest(d deps) ([]byte, error) {
	exe := d.exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, nil // cannot tell: behave as the plain engine
		}
	}
	data, err := pack.Read(exe)
	if errors.Is(err, pack.ErrNotPacked) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("this installer: %w", err)
	}
	return data, nil
}

// manifestArg returns the manifest named on the command line, or the one
// appended to the installer when none is named. More than one name, or none
// from a plain engine, is a usage error.
func manifestArg(d deps, args []string) (*manifest.Manifest, error) {
	switch len(args) {
	case 1:
		return manifest.Load(args[0])
	case 0:
		data, err := embeddedManifest(d)
		if err != nil {
			return nil, err
		}
		if data == nil {
			return nil, errUsage
		}
		return manifest.Parse(bytes.NewReader(data))
	}
	return nil, errUsage
}

// response is a file that answers the questions the window would ask, so an
// installation can run without anyone. Unknown fields are errors.
type response struct {
	Scope     string            `json:"scope,omitempty"`
	Locations map[string]string `json:"locations,omitempty"`
	Options   map[string]int    `json:"options,omitempty"`
	// Shortcuts lists the places to put a shortcut: start_menu, desktop, taskbar.
	Shortcuts []string `json:"shortcuts,omitempty"`
}

// parseShortcuts combines the response file's shortcuts with --shortcut
// (a value may hold several places separated by commas) and checks each one.
func parseShortcuts(fromFile, flags []string) ([]shortcut.Place, error) {
	var out []shortcut.Place
	seen := map[shortcut.Place]bool{}
	add := func(from, v string) error {
		for _, part := range strings.Split(v, ",") {
			p := shortcut.Place(strings.TrimSpace(part))
			if !p.Valid() {
				return fmt.Errorf("%s: %q is not a place for a shortcut (start_menu, desktop or taskbar)", from, part)
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		return nil
	}
	for _, v := range fromFile {
		if err := add("the response file", v); err != nil {
			return nil, err
		}
	}
	for _, v := range flags {
		if err := add("--shortcut", v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// parseAnswers combines the response file's options with --option id=value
// (the flag wins) and checks that each names an option of the manifest.
func parseAnswers(m *manifest.Manifest, fromFile map[string]int, sets []string) (map[string]int, error) {
	known := map[string]bool{}
	for _, o := range m.Options {
		known[o.ID] = true
	}
	out := map[string]int{}
	for id, v := range fromFile {
		if !known[id] {
			return nil, fmt.Errorf("the response file: %q is not an option of this manifest", id)
		}
		out[id] = v
	}
	for _, s := range sets {
		id, val, ok := strings.Cut(s, "=")
		n, err := strconv.Atoi(val)
		if !ok || err != nil {
			return nil, fmt.Errorf("--option %q: expected id=number", s)
		}
		if !known[id] {
			return nil, fmt.Errorf("--option %q: %q is not an option of this manifest", s, id)
		}
		out[id] = n
	}
	return out, nil
}

func readResponse(path string) (*response, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("the response file is larger than 64 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r response
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("reading the response file: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("reading the response file: unexpected data after the JSON document")
	}
	return &r, nil
}

// printer writes what a person or a script reads.
type printer struct {
	stdout, stderr io.Writer
	json, silent   bool
	enc            *json.Encoder
	seen           map[string]int // last percent printed for each component
	log            *runLog        // nil until the command knows where the record lives
}

// startLog begins the run log next to the record. Nothing is written until
// commitLog (or a failure), so a plan or a dry run leaves no file.
func (p *printer) startLog(recPath, command string) {
	p.log = newRunLog(recPath, command, func(msg string) { fprintln(p.stderr, msg) })
	p.log.add("start", map[string]any{"command": command, "version": version})
}

func (p *printer) commitLog() { p.log.commit() }

// logLine is the log path to show, or "".
func (p *printer) logLine() string { return p.log.Path() }

func newPrinter(stdout, stderr io.Writer, asJSON, silent bool) *printer {
	p := &printer{stdout: stdout, stderr: stderr, json: asJSON, silent: silent, seen: map[string]int{}}
	if asJSON {
		p.enc = json.NewEncoder(stdout)
	}
	return p
}

// event writes one JSON line (only in JSON mode).
func (p *printer) event(kind string, fields map[string]any) {
	p.log.add(kind, fields)
	if kind == "result" {
		if path := p.logLine(); path != "" {
			fields["log"] = path
			p.say("Log: %s", path)
		}
		p.log.close()
	}
	if !p.json {
		return
	}
	fields["event"] = kind
	_ = p.enc.Encode(fields)
}

// say writes a line for people, unless silent or in JSON mode.
func (p *printer) say(format string, a ...any) {
	if strings.TrimSpace(format) != "" && !strings.HasPrefix(format, "Log: ") {
		p.log.add("message", map[string]any{"text": fmt.Sprintf(format, a...)})
	}
	if p.silent || p.json {
		return
	}
	fprintf(p.stdout, format+"\n", a...)
}

// fail writes an error to stderr (in JSON mode, as an event too).
// eventNoLog writes a JSON line that is already in the log.
func (p *printer) eventNoLog(kind string, fields map[string]any) {
	if !p.json {
		return
	}
	fields["event"] = kind
	_ = p.enc.Encode(fields)
}

func (p *printer) fail(code int, rolledBack *bool, msg string) int {
	p.commitLog()
	p.log.add("result", map[string]any{"ok": false, "exit": code, "error": msg})
	path := p.logLine()
	p.log.close()
	if p.json {
		f := map[string]any{"ok": false, "exit": code, "error": msg}
		if rolledBack != nil {
			f["rolled_back"] = *rolledBack
		}
		if path != "" {
			f["log"] = path
		}
		p.eventNoLog("result", f)
	} else {
		fprintln(p.stderr, msg)
		if path != "" {
			fprintln(p.stderr, "Log: "+path)
		}
	}
	return code
}

func (p *printer) progress(ev install.Event) {
	if ev.Phase != install.PhaseDownload || ev.Done == 0 || ev.Done == ev.Total {
		p.log.add("progress", map[string]any{"phase": ev.Phase, "component": ev.Component, "file": ev.File, "done": ev.Done, "total": ev.Total})
	}
	if p.json {
		p.event("progress", map[string]any{
			"phase": ev.Phase, "component": ev.Component, "label": ev.Label, "file": ev.File,
			"done": ev.Done, "total": ev.Total, "index": ev.Index, "count": ev.Count,
		})
		return
	}
	if p.silent {
		return
	}
	switch ev.Phase {
	case install.PhaseDownload:
		pct := 0
		if ev.Total > 0 {
			pct = int(ev.Done * 100 / ev.Total)
		}
		last, started := p.seen[ev.Component]
		switch {
		case !started:
			fprintf(p.stdout, "[%d/%d] Downloading %s (%s)\n", ev.Index, ev.Count, ev.Label, checks.FormatBytes(ev.Total))
			p.seen[ev.Component] = 0
		case pct >= last+25 && pct < 100:
			fprintf(p.stdout, "        %d%%\n", pct)
			p.seen[ev.Component] = pct / 25 * 25
		}
	case install.PhaseVerify:
		fprintf(p.stdout, "        Checking %s\n", ev.Label)
	case install.PhaseUnpack:
		if ev.Done == ev.Total && ev.File == "" {
			fprintf(p.stdout, "        Unpacked %s\n", ev.Label)
		}
	}
}

func cmdInstall(ctx context.Context, d deps, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "", "user or machine (default: the manifest's)")
	respPath := fs.String("response", "", "a JSON file with the answers (scope and locations)")
	asJSON := fs.Bool("json", false, "print one JSON event per line")
	silent := fs.Bool("silent", false, "print nothing but errors")
	dry := fs.Bool("dry-run", false, "run the checks and show what would be downloaded, change nothing")
	var sets, optSets, shortSets listFlag
	fs.Var(&sets, "set", "location=path, to choose a folder instead of the default (repeatable)")
	fs.Var(&optSets, "option", "id=number, to answer an option such as a port (repeatable)")
	fs.Var(&shortSets, "shortcut", "start_menu, desktop or taskbar: where to put a shortcut (repeatable, or separated by commas)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	pr := newPrinter(stdout, stderr, *asJSON, *silent)

	m, err := manifestArg(d, fs.Args())
	if errors.Is(err, errUsage) {
		fprint(stderr, usage)
		return exitUsage
	}
	if err != nil {
		return pr.fail(exitProblem, nil, err.Error())
	}
	var resp response
	if *respPath != "" {
		r, err := readResponse(*respPath)
		if err != nil {
			return pr.fail(exitProblem, nil, err.Error())
		}
		resp = *r
	}

	answers, err := parseAnswers(m, resp.Options, optSets)
	if err != nil {
		return pr.fail(exitUsage, nil, err.Error())
	}

	places, err := parseShortcuts(resp.Shortcuts, shortSets)
	if err != nil {
		return pr.fail(exitUsage, nil, err.Error())
	}

	sc := m.Scope
	if resp.Scope != "" {
		sc = resp.Scope
	}
	if *scope != "" {
		sc = *scope
	}
	if sc != manifest.ScopeUser && sc != manifest.ScopeMachine {
		return pr.fail(exitUsage, nil, fmt.Sprintf("the scope must be %q or %q, got %q", manifest.ScopeUser, manifest.ScopeMachine, sc))
	}
	if sc == manifest.ScopeMachine && m.Scope != manifest.ScopeMachine && !m.AllowMachine {
		return pr.fail(exitProblem, nil, fmt.Sprintf("%s cannot be installed for all users", m.App.Name))
	}

	paths := map[string]string{}
	for _, l := range m.Locations {
		p, err := l.DefaultPathFor(runtime.GOOS, sc, env)
		if err != nil {
			return pr.fail(exitProblem, nil, err.Error())
		}
		paths[l.ID] = p
	}
	apply := func(id, p, from string) (string, bool) {
		if _, ok := m.Location(id); !ok {
			return fmt.Sprintf("%s: %q is not a location of this manifest", from, id), false
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return err.Error(), false
		}
		paths[id] = abs
		return "", true
	}
	for id, p := range resp.Locations {
		if msg, ok := apply(id, p, "the response file"); !ok {
			return pr.fail(exitUsage, nil, msg)
		}
	}
	for _, s := range sets {
		id, p, ok := strings.Cut(s, "=")
		if !ok {
			return pr.fail(exitUsage, nil, fmt.Sprintf("--set %q: expected location=path", s))
		}
		if msg, ok := apply(id, p, "--set"); !ok {
			return pr.fail(exitUsage, nil, msg)
		}
	}

	recPath, err := record.DefaultPath(m.App.ID, sc)
	if err != nil {
		return pr.fail(exitProblem, nil, err.Error())
	}
	pr.startLog(recPath, "install")
	pr.log.add("plan", map[string]any{"app": m.App.ID, "version": m.App.Version, "scope": sc, "os": runtime.GOOS, "arch": runtime.GOARCH, "paths": paths, "answers": answers, "dry_run": *dry})
	// A shortcut the manifest does not allow is refused before the checks run.
	if err := install.CheckShortcuts(m, places, runtime.GOOS); err != nil {
		return pr.fail(exitProblem, nil, err.Error())
	}

	// The checks come first: if one fails, nothing is installed.
	results := checks.Run(m, paths, checks.Options{Scope: sc, Elevated: checks.Elevated(), RecordPath: recPath, Answers: answers})
	failed := 0
	for _, r := range results {
		pr.event("check", map[string]any{"result": r})
		mark := map[checks.Status]string{checks.OK: "ok  ", checks.Warning: "warn", checks.Failed: "FAIL"}[r.Status]
		if r.Status == checks.Failed {
			failed++
			if pr.silent && !pr.json {
				fprintf(stderr, "[%s] %s: %s\n", mark, r.Title, r.Detail)
			}
		}
		pr.say("[%s] %s: %s", mark, r.Title, r.Detail)
	}
	if failed > 0 {
		return pr.fail(exitProblem, nil, fmt.Sprintf("%d check(s) failed; nothing was installed", failed))
	}

	if *dry {
		pr.say("")
		pr.say("Dry run: nothing was changed. This would install %s %s for the %s:", m.App.Name, m.App.Version, sc)
		for _, c := range m.ComponentsFor(runtime.GOOS, runtime.GOARCH) {
			pr.say("  %s (%s) from %s into %s", c.Label, checks.FormatBytes(c.Size), c.URL, paths[c.Location])
		}
		for _, cf := range m.Config {
			pr.say("  would write %s in %s", cf.Name, paths[cf.Location])
		}
		for _, place := range places {
			if place == shortcut.Taskbar {
				pr.say("  would not pin to the taskbar: %v", shortcut.ErrTaskbar)
				continue
			}
			spec, err := install.ShortcutSpec(m, place, sc, runtime.GOOS, paths)
			if err != nil {
				return pr.fail(exitProblem, nil, err.Error())
			}
			path, err := shortcut.Path(spec, shortcut.Env{GOOS: runtime.GOOS})
			if err != nil {
				pr.say("  would not make a %s shortcut: %v", place, err)
				continue
			}
			pr.say("  would make a %s shortcut at %s", place, path)
		}
		pr.event("result", map[string]any{"ok": true, "dry_run": true, "exit": exitOK})
		return exitOK
	}

	pr.commitLog()
	opt := install.Options{
		Client: d.client, Paths: paths, Scope: sc, RecordPath: recPath, Answers: answers, Shortcuts: places,
		Events: pr.progress,
		Log: func(s string) {
			pr.event("log", map[string]any{"message": s})
		},
	}
	res, err := install.Run(ctx, m, opt)
	if err != nil {
		var ie *install.Error
		if errors.As(err, &ie) {
			code := exitFailedRolledBack
			msg := err.Error()
			if !ie.RolledBack {
				code = exitFailedPartial
				msg += fmt.Sprintf("; the record %s lists what is left: run uninstall --yes %s", recPath, recPath)
				for _, f := range ie.Failures {
					msg += fmt.Sprintf("\n  could not remove %s: %v", f.Path, f.Err)
				}
			}
			return pr.fail(code, &ie.RolledBack, msg)
		}
		return pr.fail(exitProblem, nil, err.Error())
	}
	files := 0
	for _, e := range res.Record.Entries {
		if e.Kind == record.KindFile && e.Owner == record.OwnerInstaller {
			files++
		}
	}
	pr.say("")
	pr.say("Installed %s %s: %d file(s) written, %d component(s) already in place. Record: %s", m.App.Name, m.App.Version, files, len(res.Skipped), recPath)
	program := ""
	if loc, rel, ok := m.LaunchPath(runtime.GOOS); ok {
		program = filepath.Join(paths[loc], rel)
		pr.say("Program: %s", program)
	}
	for _, p := range res.Shortcuts {
		pr.say("Shortcut: %s", p)
	}
	for _, n := range res.ShortcutNotes {
		pr.say("Note: %s", n)
	}
	pr.event("result", map[string]any{"ok": true, "exit": exitOK, "record": recPath, "files": files, "skipped": res.Skipped, "shortcuts": res.Shortcuts, "notes": res.ShortcutNotes, "program": program})
	return exitOK
}

func cmdUninstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "user", "user or machine, to find the record of an app id")
	yes := fs.Bool("yes", false, "really remove; without it only the plan is shown")
	asJSON := fs.Bool("json", false, "print one JSON event per line")
	silent := fs.Bool("silent", false, "print nothing but errors")
	var keepLoc, keepPath listFlag
	fs.Var(&keepLoc, "keep-location", "keep what the installer made in this location (repeatable)")
	fs.Var(&keepPath, "keep-path", "keep this one entry (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fprint(stderr, usage)
		return exitUsage
	}
	pr := newPrinter(stdout, stderr, *asJSON, *silent)

	target := fs.Arg(0)
	recPath := target
	if _, err := os.Stat(target); err != nil && !strings.ContainsAny(target, `/\`) && !strings.HasSuffix(target, ".json") {
		p, perr := record.DefaultPath(target, *scope)
		if perr != nil {
			return pr.fail(exitProblem, nil, perr.Error())
		}
		recPath = p
	}
	r, err := record.Load(recPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pr.fail(exitProblem, nil, fmt.Sprintf("no installation record at %s: nothing to remove", recPath))
		}
		return pr.fail(exitProblem, nil, err.Error())
	}

	pr.startLog(recPath, "uninstall")
	c := record.Choices{KeepLocation: map[string]bool{}, KeepPath: map[string]bool{}}
	for _, l := range keepLoc {
		c.KeepLocation[l] = true
	}
	for _, p := range keepPath {
		c.KeepPath[filepath.Clean(p)] = true
	}
	items := r.Plan(c)
	rm, keep, prot := record.Totals(items)
	for _, it := range items {
		pr.event("plan", map[string]any{"action": it.Action, "path": it.Path, "kind": it.Kind, "size": it.Size, "reason": it.Reason})
		pr.say("%-9s %s (%s)", it.Action, it.Path, it.Reason)
	}
	pr.say("")
	pr.say("Would free %s, keep %s, leave %s that is not the installer's.", checks.FormatBytes(rm), checks.FormatBytes(keep), checks.FormatBytes(prot))
	if !*yes {
		pr.say("Nothing was removed. Run again with --yes to remove.")
		pr.event("result", map[string]any{"ok": true, "removed": false, "exit": exitOK})
		return exitOK
	}

	pr.commitLog()
	rep := r.Apply(items)
	for _, p := range rep.Removed {
		r.Remove(p)
		pr.log.add("removed", map[string]any{"path": p})
	}
	for _, f := range rep.Failed {
		pr.log.add("not_removed", map[string]any{"path": f.Path, "error": f.Err.Error()})
	}
	if len(rep.Failed) > 0 {
		_ = r.Save(recPath) // what is left stays on record, so the job can be finished
		msg := fmt.Sprintf("%d item(s) could not be removed; the record at %s lists what is left:", len(rep.Failed), recPath)
		for _, f := range rep.Failed {
			msg += fmt.Sprintf("\n  %s: %v", f.Path, f.Err)
		}
		return pr.fail(exitLeftovers, nil, msg)
	}
	_ = os.Remove(recPath)
	_ = os.Remove(filepath.Dir(recPath)) // only if empty
	pr.say("Removed %d item(s). Kept %d, left %d that is not the installer's.", len(rep.Removed), len(rep.Kept), len(rep.Protected))
	pr.event("result", map[string]any{"ok": true, "removed": true, "exit": exitOK, "removed_count": len(rep.Removed), "kept": rep.Kept, "protected": rep.Protected})
	return exitOK
}
