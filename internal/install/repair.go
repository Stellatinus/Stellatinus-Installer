package install

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
	"github.com/Stellatinus/Stellatinus-Installer/internal/safepath"
)

// Errors that say a repair cannot even start.
var (
	// ErrDifferentVersion is returned when the manifest is for another version
	// than the one installed: repair puts back what was installed, it does not
	// update (that is the updater's job).
	ErrDifferentVersion = errors.New("the installed version is not the one this manifest describes; repair restores the installed version, it does not update")
	// ErrNoPaths is returned for a record made before repair existed: it does not
	// say which folders were chosen.
	ErrNoPaths = errors.New("this record does not say which folders were chosen (it was made by an older installer); remove the installation and install it again")
)

// ProblemKind says what is wrong with a recorded file.
type ProblemKind string

// Problem kinds.
const (
	// ProblemMissing: the file or folder is not there.
	ProblemMissing ProblemKind = "missing"
	// ProblemChanged: the file is there but its size or hash is not what was
	// installed.
	ProblemChanged ProblemKind = "changed"
	// ProblemConflict: something stands where the entry should be (a folder where
	// a file goes, a link, a path that leads through a link). Repair never
	// touches it.
	ProblemConflict ProblemKind = "conflict"
)

// Problem is one thing repair found wrong.
type Problem struct {
	Path      string      `json:"path"`
	Kind      ProblemKind `json:"kind"`
	Component string      `json:"component"`
	Reason    string      `json:"reason"`
	// Fixable says whether repair would put it back. A conflict never is, and a
	// changed settings file is not a problem at all (it is the user's).
	Fixable bool `json:"fixable"`
}

// RepairResult is what Repair did.
type RepairResult struct {
	Fixed  []string         // files and folders put back
	Failed []record.Failure // what could not be put back, and why
}

// repairRunner builds what the install code needs, from a record that was made
// by an installation of this manifest.
func repairRunner(m *manifest.Manifest, rec *record.Record, opt *Options) (*runner, error) {
	opt.defaults()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if rec.AppID != m.App.ID {
		return nil, fmt.Errorf("the record is for %q, the manifest for %q", rec.AppID, m.App.ID)
	}
	if rec.AppVersion != m.App.Version {
		return nil, fmt.Errorf("%w (installed %s, manifest %s)", ErrDifferentVersion, rec.AppVersion, m.App.Version)
	}
	for _, l := range m.Locations {
		if p, ok := rec.Paths[l.ID]; !ok || !filepath.IsAbs(p) {
			return nil, ErrNoPaths
		}
	}
	paths := make(map[string]string, len(rec.Paths))
	for id, p := range rec.Paths {
		paths[id] = filepath.Clean(p)
	}
	return &runner{m: m, comps: m.ComponentsFor(opt.GOOS, opt.GOARCH), answers: rec.Answers, opt: *opt, rec: rec, paths: paths}, nil
}

// Verify checks every file and folder the installer made against what is on
// disk: it exists, is a regular file, and has the size and hash that were
// recorded. It changes nothing. A file that is adopted, one an app registered
// later, and one that is not part of a component of this manifest is not the
// installer's to judge and is not checked. A settings file that was edited is
// the user's and is accepted; one that was deleted is a problem.
func Verify(m *manifest.Manifest, rec *record.Record, opt Options) (problems []Problem, checked int, err error) {
	r, err := repairRunner(m, rec, &opt)
	if err != nil {
		return nil, 0, err
	}
	return r.verify()
}

func (r *runner) knownOrigin(origin string) bool {
	if origin == "config" {
		return true
	}
	for _, c := range r.comps {
		if c.ID == origin {
			return true
		}
	}
	return false
}

func (r *runner) verify() ([]Problem, int, error) {
	var problems []Problem
	checked := 0
	for _, e := range r.rec.Entries {
		// Folders carry no component and are made again when a file needs them.
		if e.Owner != record.OwnerInstaller || e.Kind != record.KindFile || !r.knownOrigin(e.Origin) {
			continue
		}
		checked++
		p := Problem{Path: e.Path, Component: e.Origin}
		if err := r.rec.Contained(e.Location, e.Path); err != nil {
			p.Kind, p.Reason = ProblemConflict, err.Error()
			problems = append(problems, p)
			continue
		}
		info, err := os.Lstat(e.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			p.Kind, p.Reason, p.Fixable = ProblemMissing, "it is not there", true
		case err != nil:
			p.Kind, p.Reason = ProblemConflict, err.Error()
		case !info.Mode().IsRegular():
			p.Kind, p.Reason = ProblemConflict, "it should be a file and is a link, a folder or a special file; left alone"
		case e.Origin == "config":
			continue // edited settings are the user's
		case info.Size() != e.Size:
			p.Kind, p.Reason, p.Fixable = ProblemChanged, fmt.Sprintf("its size is %d bytes, it was installed with %d", info.Size(), e.Size), true
		default:
			if e.SHA256 == "" {
				continue
			}
			sum, err := fileSHA256(e.Path)
			if err != nil {
				p.Kind, p.Reason = ProblemConflict, err.Error()
			} else if sum != e.SHA256 {
				p.Kind, p.Reason, p.Fixable = ProblemChanged, "its content is not what was installed", true
			} else {
				continue
			}
		}
		problems = append(problems, p)
	}
	return problems, checked, nil
}

// Repair puts back what Verify found missing or changed, and nothing else: a
// component with a problem is downloaded again (checked like an installation),
// and only the damaged files are written, each through a temporary name and
// renamed into place, so a file is never half written. Files that are not the
// installer's, and files of a component that are intact, are not touched; a
// conflict is reported and left. The record is saved after every component.
// A component that cannot be restored is reported in Failed and the others
// still are.
func Repair(ctx context.Context, m *manifest.Manifest, rec *record.Record, opt Options) (*RepairResult, error) {
	r, err := repairRunner(m, rec, &opt)
	if err != nil {
		return nil, err
	}
	problems, _, err := r.verify()
	if err != nil {
		return nil, err
	}
	res := &RepairResult{}
	byComp := map[string][]Problem{}
	var order []string
	for _, p := range problems {
		if !p.Fixable {
			continue
		}
		if _, ok := byComp[p.Component]; !ok {
			order = append(order, p.Component)
		}
		byComp[p.Component] = append(byComp[p.Component], p)
	}
	r.count = len(order)
	for i, id := range order {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		fixed, failed := r.repairComponent(ctx, i, len(order), id, byComp[id])
		res.Fixed = append(res.Fixed, fixed...)
		res.Failed = append(res.Failed, failed...)
		if err := r.save(); err != nil {
			return res, err
		}
	}
	r.emit(Event{Phase: PhaseDone, Index: len(order), Count: len(order)})
	return res, nil
}

func (r *runner) repairComponent(ctx context.Context, i, count int, id string, ps []Problem) (fixed []string, failed []record.Failure) {
	fail := func(err error) ([]string, []record.Failure) {
		for _, p := range ps {
			failed = append(failed, record.Failure{Path: p.Path, Err: err})
		}
		return nil, failed
	}
	if id == "config" {
		for _, p := range ps {
			if err := r.restoreConfig(p); err != nil {
				failed = append(failed, record.Failure{Path: p.Path, Err: err})
			} else {
				fixed = append(fixed, p.Path)
			}
		}
		return fixed, failed
	}
	var c manifest.Component
	for _, x := range r.comps {
		if x.ID == id {
			c = x
		}
	}
	base := r.paths[c.Location]
	if c.Dest != "" {
		rel, err := safepath.CleanRel(c.Dest)
		if err != nil {
			return fail(err)
		}
		base = filepath.Join(base, rel)
	}
	var err error
	switch c.Kind {
	case manifest.ComponentZip:
		err = r.repairZip(ctx, i, c, base, ps)
	default:
		err = r.repairFile(ctx, i, c, base)
	}
	if err != nil {
		return fail(err)
	}
	for _, p := range ps {
		fixed = append(fixed, p.Path)
	}
	return fixed, nil
}

// repairFile downloads a single-file component again and puts it where it was.
func (r *runner) repairFile(ctx context.Context, idx int, c manifest.Component, base string) error {
	if _, err := r.makeDirs(c.Location, base, false); err != nil {
		return err
	}
	dest := filepath.Join(base, c.Name)
	part := filepath.Join(base, ".stellatinus-"+c.ID+".part")
	if err := r.track(record.Entry{Path: part, Kind: record.KindFile, Owner: record.OwnerInstaller, Location: c.Location, Origin: c.ID}); err != nil {
		return err
	}
	if err := r.save(); err != nil {
		return err
	}
	defer func() { _ = os.Remove(part); r.untrack(part) }()
	if err := r.fetch(ctx, idx, c, part); err != nil {
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	r.untrack(part)
	r.logf("restored %s", dest)
	return r.rec.Add(record.Entry{Path: dest, Kind: record.KindFile, Owner: record.OwnerInstaller, Size: c.Size, SHA256: c.SHA256, Location: c.Location, Origin: c.ID})
}

// repairZip downloads a zip component again, checks the whole archive as an
// installation does, and writes only the entries whose files are damaged.
func (r *runner) repairZip(ctx context.Context, idx int, c manifest.Component, base string, ps []Problem) error {
	want := map[string]bool{}
	for _, p := range ps {
		want[filepath.Clean(p.Path)] = true
	}
	if _, err := r.makeDirs(c.Location, base, false); err != nil {
		return err
	}
	part := filepath.Join(base, ".stellatinus-"+c.ID+".zip.part")
	if err := r.track(record.Entry{Path: part, Kind: record.KindFile, Owner: record.OwnerInstaller, Location: c.Location, Origin: c.ID}); err != nil {
		return err
	}
	if err := r.save(); err != nil {
		return err
	}
	defer func() { _ = os.Remove(part); r.untrack(part) }()
	if err := r.fetch(ctx, idx, c, part); err != nil {
		return err
	}
	zr, err := zip.OpenReader(part)
	if err != nil {
		return fmt.Errorf("%s is not a readable zip archive: %w", c.Label, err)
	}
	defer func() { _ = zr.Close() }()
	targets, _, err := r.planZip(zr, base, true)
	if err != nil {
		return fmt.Errorf("%s: %w", c.Label, err)
	}
	found := 0
	for _, t := range targets {
		if !want[t.target] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := r.makeDirs(c.Location, filepath.Dir(t.target), false); err != nil {
			return err
		}
		if _, err := r.writeEntry(c, t); err != nil {
			return err
		}
		r.logf("restored %s", t.target)
		found++
	}
	if found != len(want) {
		return fmt.Errorf("%s no longer holds %d of the files to restore", c.Label, len(want)-found)
	}
	return nil
}

// restoreConfig writes a deleted settings file again, from the answers the
// record kept.
func (r *runner) restoreConfig(p Problem) error {
	for _, cf := range r.m.Config {
		if filepath.Join(r.paths[cf.Location], cf.Name) == filepath.Clean(p.Path) {
			return r.writeConfigFile(cf)
		}
	}
	return errors.New("the manifest no longer asks for this file")
}
