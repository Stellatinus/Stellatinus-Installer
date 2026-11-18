// Command stellatinus-installer is the command line face of the installer
// engine. It validates a manifest, derives and checks an accent palette, runs
// the checks that come before an installation, installs an app from its
// manifest (downloading, checking and recording what it writes, and rolling
// back on failure), and removes an installation by its record, only on
// request. It has no window; that part is tracked in the issues.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Stellatinus/Stellatinus-Installer/internal/checks"
	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
	"github.com/Stellatinus/Stellatinus-Installer/internal/theme"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `stellatinus-installer: the shared installer engine (command line)

Usage:
  stellatinus-installer version
  stellatinus-installer validate <manifest.json>
  stellatinus-installer accent <#rrggbb>
  stellatinus-installer check [--scope user|machine] [--set location=path]... [--option id=number]... [--record file] [--json] <manifest.json>
  stellatinus-installer plan [--keep-location id]... [--keep-path path]... [--json] <record.json>
  stellatinus-installer install [--scope user|machine] [--set location=path]... [--option id=number]... [--response file] [--silent] [--json] [--dry-run] <manifest.json>
  stellatinus-installer uninstall [--scope user|machine] [--keep-location id]... [--keep-path path]... [--yes] [--silent] [--json] <record.json | app-id>

Options go before the file name. "uninstall" only shows the plan unless --yes is given.

Exit status: 0 success, 1 a problem was found and nothing was changed, 2 wrong usage,
3 an installation failed and was rolled back, 4 it failed and something could not be
rolled back, 5 a removal left items it could not remove. See docs/cli.md.
`

func main() {
	// Ctrl+C or a termination request cancels a running installation, which
	// then rolls back what it created.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runWith(ctx, deps{}, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func run(args []string, stdout, stderr io.Writer) int {
	return runWith(context.Background(), deps{}, args, stdout, stderr)
}

func runWith(ctx context.Context, d deps, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fprintln(stdout, "stellatinus-installer", version)
		return 0
	case "help", "-h", "--help":
		fprint(stdout, usage)
		return 0
	case "validate":
		return cmdValidate(args[1:], stdout, stderr)
	case "accent":
		return cmdAccent(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "plan":
		return cmdPlan(args[1:], stdout, stderr)
	case "install":
		return cmdInstall(ctx, d, args[1:], stdout, stderr)
	case "uninstall":
		return cmdUninstall(args[1:], stdout, stderr)
	}
	fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return 2
}

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fprint(stderr, usage)
		return 2
	}
	m, err := manifest.Load(args[0])
	if err != nil {
		var me *manifest.Error
		if errors.As(err, &me) {
			fprintf(stderr, "%s: %d problem(s)\n", args[0], len(me.Issues))
			for _, i := range me.Issues {
				fprintf(stderr, "  - %s\n", i)
			}
		} else {
			fprintf(stderr, "%s: %v\n", args[0], err)
		}
		return 1
	}
	fprintf(stdout, "%s: valid (%s %s, %d location(s), %d component(s))\n", args[0], m.App.Name, m.App.Version, len(m.Locations), len(m.Components))
	return 0
}

func cmdAccent(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fprint(stderr, usage)
		return 2
	}
	bad := false
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		p, err := theme.Derive(args[0], dark)
		if p.Fill != "" {
			fprintf(stdout, "%-5s fill %s  ink %s (%.1f:1)  text %s (%.1f:1)  soft %s\n", name, p.Fill, p.Ink, p.InkContrast, p.Text, p.TextContrast, p.Soft)
		}
		if err != nil {
			fprintf(stderr, "%v\n", err)
			bad = true
		}
	}
	if bad {
		return 1
	}
	return 0
}

// env reads an environment variable, falling back to the home folder for the
// variables manifests use for it.
func env(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	if name == "HOME" || name == "USERPROFILE" {
		h, _ := os.UserHomeDir()
		return h
	}
	return ""
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "", "user or machine (default: the manifest's)")
	recordPath := fs.String("record", "", "installation record to look for an earlier installation")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	var sets, optSets listFlag
	fs.Var(&sets, "set", "location=path, to choose a folder instead of the default (repeatable)")
	fs.Var(&optSets, "option", "id=number, to answer an option such as a port (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fprint(stderr, usage)
		return 2
	}
	m, err := manifest.Load(fs.Arg(0))
	if err != nil {
		fprintln(stderr, err)
		return 1
	}
	paths := map[string]string{}
	for _, l := range m.Locations {
		p, err := l.DefaultPath(runtime.GOOS, env)
		if err != nil {
			fprintln(stderr, err)
			return 1
		}
		paths[l.ID] = p
	}
	for _, s := range sets {
		id, p, ok := strings.Cut(s, "=")
		if _, known := m.Location(id); !ok || !known {
			fprintf(stderr, "--set %q: expected location=path with a location from the manifest\n", s)
			return 2
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			fprintln(stderr, err)
			return 2
		}
		paths[id] = abs
	}
	if *scope != "" && *scope != manifest.ScopeUser && *scope != manifest.ScopeMachine {
		fprintf(stderr, "--scope must be %q or %q\n", manifest.ScopeUser, manifest.ScopeMachine)
		return 2
	}
	answers, err := parseAnswers(m, nil, optSets)
	if err != nil {
		fprintln(stderr, err)
		return 2
	}
	results := checks.Run(m, paths, checks.Options{Scope: *scope, Elevated: checks.Elevated(), RecordPath: *recordPath, Answers: answers})
	failed := 0
	for _, r := range results {
		if !r.Passed() {
			failed++
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
	} else {
		for _, r := range results {
			mark := map[checks.Status]string{checks.OK: "ok  ", checks.Warning: "warn", checks.Failed: "FAIL"}[r.Status]
			fprintf(stdout, "[%s] %s: %s\n", mark, r.Title, r.Detail)
			if r.Status == checks.Failed && r.GoBack != "" {
				fprintf(stdout, "       fix: go back to the step for %q\n", r.GoBack)
			} else if r.Status == checks.Failed && r.Fix == checks.FixElevate {
				fprintln(stdout, "       fix: restart as administrator")
			}
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func cmdPlan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the plan as JSON")
	var keepLoc, keepPath listFlag
	fs.Var(&keepLoc, "keep-location", "keep what the installer made in this location (repeatable)")
	fs.Var(&keepPath, "keep-path", "keep this one entry (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fprint(stderr, usage)
		return 2
	}
	r, err := record.Load(fs.Arg(0))
	if err != nil {
		fprintln(stderr, err)
		return 1
	}
	c := record.Choices{KeepLocation: map[string]bool{}, KeepPath: map[string]bool{}}
	for _, l := range keepLoc {
		c.KeepLocation[l] = true
	}
	for _, p := range keepPath {
		c.KeepPath[filepath.Clean(p)] = true
	}
	items := r.Plan(c)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(items)
		return 0
	}
	for _, it := range items {
		fprintf(stdout, "%-9s %s (%s)\n", it.Action, it.Path, it.Reason)
	}
	rm, keep, prot := record.Totals(items)
	fprintf(stdout, "\nwould free %s, keep %s, leave %s that is not the installer's\n",
		checks.FormatBytes(rm), checks.FormatBytes(keep), checks.FormatBytes(prot))
	fprintln(stdout, "(this is a plan only: nothing was removed)")
	return 0
}

// Writing to the terminal can fail (a closed pipe); there is nothing useful to
// do about it, so these helpers drop the error on purpose.
func fprintf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func fprintln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }
func fprint(w io.Writer, a ...any)                 { _, _ = fmt.Fprint(w, a...) }
