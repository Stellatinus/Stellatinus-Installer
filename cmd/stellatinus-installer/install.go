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
	"strings"

	"github.com/Stellatinus/Stellatinus-Installer/internal/checks"
	"github.com/Stellatinus/Stellatinus-Installer/internal/install"
	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
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
}

// response is a file that answers the questions the window would ask, so an
// installation can run without anyone. Unknown fields are errors.
type response struct {
	Scope     string            `json:"scope,omitempty"`
	Locations map[string]string `json:"locations,omitempty"`
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
}

func newPrinter(stdout, stderr io.Writer, asJSON, silent bool) *printer {
	p := &printer{stdout: stdout, stderr: stderr, json: asJSON, silent: silent, seen: map[string]int{}}
	if asJSON {
		p.enc = json.NewEncoder(stdout)
	}
	return p
}

// event writes one JSON line (only in JSON mode).
func (p *printer) event(kind string, fields map[string]any) {
	if !p.json {
		return
	}
	fields["event"] = kind
	_ = p.enc.Encode(fields)
}

// say writes a line for people, unless silent or in JSON mode.
func (p *printer) say(format string, a ...any) {
	if p.silent || p.json {
		return
	}
	fprintf(p.stdout, format+"\n", a...)
}

// fail writes an error to stderr (in JSON mode, as an event too).
func (p *printer) fail(code int, rolledBack *bool, msg string) int {
	if p.json {
		f := map[string]any{"ok": false, "exit": code, "error": msg}
		if rolledBack != nil {
			f["rolled_back"] = *rolledBack
		}
		p.event("result", f)
	} else {
		fprintln(p.stderr, msg)
	}
	return code
}

func (p *printer) progress(ev install.Event) {
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
	var sets listFlag
	fs.Var(&sets, "set", "location=path, to choose a folder instead of the default (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fprint(stderr, usage)
		return exitUsage
	}
	pr := newPrinter(stdout, stderr, *asJSON, *silent)

	m, err := manifest.Load(fs.Arg(0))
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
		p, err := l.DefaultPath(runtime.GOOS, env)
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

	// The checks come first: if one fails, nothing is installed.
	results := checks.Run(m, paths, checks.Options{Scope: sc, Elevated: checks.Elevated(), RecordPath: recPath})
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
		for _, c := range m.Components {
			pr.say("  %s (%s) from %s into %s", c.Label, checks.FormatBytes(c.Size), c.URL, paths[c.Location])
		}
		pr.event("result", map[string]any{"ok": true, "dry_run": true, "exit": exitOK})
		return exitOK
	}

	opt := install.Options{
		Client: d.client, Paths: paths, Scope: sc, RecordPath: recPath,
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
	pr.event("result", map[string]any{"ok": true, "exit": exitOK, "record": recPath, "files": files, "skipped": res.Skipped})
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

	rep := r.Apply(items)
	for _, p := range rep.Removed {
		r.Remove(p)
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
