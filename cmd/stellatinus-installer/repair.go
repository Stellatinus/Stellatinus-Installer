package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Stellatinus/Stellatinus-Installer/internal/install"
	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

// exitRepairLeft: the repair could not put everything back (a download failed,
// a conflict was left, or it was interrupted). Nothing was lost; run it again.
const exitRepairLeft = 6

// cmdRepair checks an installation against its record and, with --yes, puts
// back the files that are missing or changed.
func cmdRepair(ctx context.Context, d deps, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "", "user or machine, to find the record (default: the manifest's)")
	yes := fs.Bool("yes", false, "really repair; without it only the problems are shown")
	asJSON := fs.Bool("json", false, "print one JSON event per line")
	silent := fs.Bool("silent", false, "print nothing but errors")
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
	sc := m.Scope
	if *scope != "" {
		sc = *scope
	}
	if sc != manifest.ScopeUser && sc != manifest.ScopeMachine {
		return pr.fail(exitUsage, nil, fmt.Sprintf("the scope must be %q or %q, got %q", manifest.ScopeUser, manifest.ScopeMachine, sc))
	}
	recPath, err := record.DefaultPath(m.App.ID, sc)
	if err != nil {
		return pr.fail(exitProblem, nil, err.Error())
	}
	rec, err := record.Load(recPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pr.fail(exitProblem, nil, fmt.Sprintf("no installation record at %s: %s is not installed for the %s, so there is nothing to repair", recPath, m.App.Name, sc))
		}
		return pr.fail(exitProblem, nil, err.Error())
	}

	pr.startLog(recPath, "repair")
	opt := install.Options{Client: d.client, RecordPath: recPath, Events: pr.progress, Log: func(s string) { pr.event("log", map[string]any{"message": s}) }}
	problems, checked, err := install.Verify(m, rec, opt)
	if err != nil {
		return pr.fail(exitProblem, nil, err.Error())
	}
	fixable := 0
	for _, p := range problems {
		pr.event("problem", map[string]any{"path": p.Path, "kind": p.Kind, "component": p.Component, "reason": p.Reason, "fixable": p.Fixable})
		mark := "left"
		if p.Fixable {
			fixable++
			mark = "repair"
		}
		pr.say("%-8s %-8s %s (%s)", p.Kind, mark, p.Path, p.Reason)
	}
	if len(problems) == 0 {
		pr.say("Nothing to repair: %d file(s) checked, all intact.", checked)
		pr.event("result", map[string]any{"ok": true, "exit": exitOK, "checked": checked, "problems": 0})
		return exitOK
	}
	pr.say("")
	pr.say("%d of %d file(s) have a problem, %d can be put back; the rest is left alone because something else stands in its place.", len(problems), checked, fixable)
	if !*yes || fixable == 0 {
		msg := "Nothing was changed."
		if fixable > 0 {
			msg += " Run again with --yes to repair."
		}
		pr.say("%s", msg)
		pr.event("result", map[string]any{"ok": false, "exit": exitProblem, "checked": checked, "problems": len(problems), "fixable": fixable, "repaired": false})
		if pr.silent && !pr.json {
			fprintf(stderr, "%d file(s) have a problem; %s\n", len(problems), msg)
		}
		return exitProblem
	}

	pr.commitLog()
	res, err := install.Repair(ctx, m, rec, opt)
	if err != nil {
		// Interrupted, or the record could not be saved: say what is known.
		msg := fmt.Sprintf("the repair stopped: %v; run it again to finish", err)
		return pr.fail(exitRepairLeft, nil, msg)
	}
	left := len(res.Failed) + (len(problems) - fixable)
	if len(res.Failed) > 0 {
		msg := fmt.Sprintf("%d file(s) could not be put back (run repair again):", len(res.Failed))
		for _, f := range res.Failed {
			msg += fmt.Sprintf("\n  %s: %v", f.Path, f.Err)
		}
		return pr.fail(exitRepairLeft, nil, msg)
	}
	pr.say("Repaired %d file(s).", len(res.Fixed))
	if left > 0 {
		return pr.fail(exitRepairLeft, nil, fmt.Sprintf("repaired %d file(s); %d more are left alone because something else stands where they go", len(res.Fixed), left))
	}
	pr.event("result", map[string]any{"ok": true, "exit": exitOK, "checked": checked, "problems": len(problems), "repaired": len(res.Fixed)})
	return exitOK
}
