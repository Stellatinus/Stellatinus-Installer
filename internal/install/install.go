// Package install downloads the components of a manifest, checks each one
// against its sha256, writes them into the chosen folders and records every
// file and folder it creates, so the installation can be removed. If anything
// fails it removes what this run created and leaves everything that was there
// before untouched. It creates the shortcuts it is asked for (see package
// shortcut) and records them. It does not show anything or decide which
// folders or shortcuts to use, and it does not update an existing installation: a
// second run over a recorded installation is refused.
package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
	"github.com/Stellatinus/Stellatinus-Installer/internal/safepath"
	"github.com/Stellatinus/Stellatinus-Installer/internal/shortcut"
)

// Phase is what the installer is doing.
type Phase string

// Phases, in the order they happen for each component.
const (
	PhasePrepare  Phase = "prepare"
	PhaseDownload Phase = "download"
	PhaseVerify   Phase = "verify"
	PhaseUnpack   Phase = "unpack"
	PhaseConfig   Phase = "config"
	PhaseDone     Phase = "done"
)

// Event is one progress report. Label is the plain sentence for the progress
// bar ("the core"); File is the real name, for the advanced log only.
type Event struct {
	Phase     Phase  `json:"phase"`
	Component string `json:"component,omitempty"`
	Label     string `json:"label,omitempty"`
	File      string `json:"file,omitempty"`
	Done      int64  `json:"done"`
	Total     int64  `json:"total"`
	Index     int    `json:"index"` // 1-based position of the component
	Count     int    `json:"count"`
}

// Options are what Run needs besides the manifest.
type Options struct {
	// Client downloads; nil uses a client with sensible timeouts. Whatever the
	// client, a redirect to anything but https is refused.
	Client *http.Client
	// Paths is the folder chosen for each location id. Every location of the
	// manifest needs one, and it must be absolute.
	Paths map[string]string
	Scope string
	// RecordPath is where the installation record is saved after every step;
	// empty keeps it in memory only. A record already there is refused.
	RecordPath string

	Events      func(Event)   // progress; may be nil
	Log         func(string)  // lines for the advanced log; may be nil
	IdleTimeout time.Duration // no bytes for this long is a stall (default 30 s)
	Attempts    int           // tries per download (default 4)
	Backoff     func(attempt int) time.Duration
	MaxUnpacked int64 // most bytes a zip may unpack to (default 8 GiB)
	MaxEntries  int   // most entries a zip may have (default 100000)

	// GOOS and GOARCH choose which components are installed (default: this
	// system). Answers holds the value of each option by id; a missing one uses
	// the manifest's default.
	GOOS, GOARCH string
	Answers      map[string]int

	// Shortcuts are the places to put a shortcut to the program the manifest's
	// launch names. Each must be offered by the manifest. Env reads the
	// environment variables that decide where they go (default os.Getenv).
	Shortcuts []shortcut.Place
	Env       func(string) string
}

// Result is what a successful run leaves behind.
type Result struct {
	Record *record.Record
	// Skipped lists components whose file was already in place with the right
	// hash, so nothing was downloaded.
	Skipped []string
	// Shortcuts lists the shortcuts that were created; ShortcutNotes says, in a
	// sentence each, why a requested one was not (it was already there, the
	// desktop folder does not exist, the taskbar).
	Shortcuts     []string
	ShortcutNotes []string
}

// Error is returned when an installation fails. RolledBack says whether
// everything this run created was removed; if not, Failures lists what is left
// and the record (when saved) lists it too.
type Error struct {
	Err        error
	RolledBack bool
	Failures   []record.Failure
}

func (e *Error) Error() string {
	if e.RolledBack {
		return fmt.Sprintf("installation failed and was rolled back: %v", e.Err)
	}
	return fmt.Sprintf("installation failed and %d item(s) could not be removed: %v", len(e.Failures), e.Err)
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error { return e.Err }

// ErrAlreadyInstalled is returned when a record already exists at RecordPath.
var ErrAlreadyInstalled = errors.New("an installation is already recorded here; remove it first (repair and update are not built yet)")

func (o *Options) defaults() {
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 30 * time.Second
	}
	if o.Attempts <= 0 {
		o.Attempts = 4
	}
	if o.Backoff == nil {
		o.Backoff = func(attempt int) time.Duration { return time.Duration(1<<(attempt-1)) * time.Second }
	}
	if o.MaxUnpacked <= 0 {
		o.MaxUnpacked = 8 << 30
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = 100000
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	o.Client = httpsOnly(o.Client)
}

type runner struct {
	m       *manifest.Manifest
	comps   []manifest.Component // the components for this system
	answers map[string]int
	opt     Options
	rec     *record.Record
	paths   map[string]string // folder chosen for each location
	created []record.Entry    // what this run created, in order
	skipped []string

	shortcuts []string // created
	notes     []string // requested and not created, with the reason
}

// Run installs every component of the manifest.
func Run(ctx context.Context, m *manifest.Manifest, opt Options) (*Result, error) {
	opt.defaults()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	for _, l := range m.Locations {
		p, ok := opt.Paths[l.ID]
		if !ok {
			return nil, fmt.Errorf("no folder was chosen for %q", l.ID)
		}
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("the folder for %q must be an absolute path, got %q", l.ID, p)
		}
	}
	if opt.RecordPath != "" {
		if _, err := os.Lstat(opt.RecordPath); err == nil {
			return nil, ErrAlreadyInstalled
		}
	}
	answers, err := resolveAnswers(m, opt.Answers)
	if err != nil {
		return nil, err
	}
	scope := opt.Scope
	if scope == "" {
		scope = m.Scope
	}
	if err := checkShortcuts(m, opt); err != nil {
		return nil, err
	}
	r := &runner{m: m, comps: m.ComponentsFor(opt.GOOS, opt.GOARCH), answers: answers, opt: opt, paths: map[string]string{}}
	for id, p := range opt.Paths {
		r.paths[id] = filepath.Clean(p)
	}
	// The record's location roots are filled in as folders are made, because a
	// root is the highest folder this run created (or the folder that existed).
	r.rec = record.New(m.App.ID, m.App.Version, scope, nil)

	if err := r.prepare(); err != nil {
		return nil, r.fail(err)
	}
	if err := r.save(); err != nil {
		return nil, r.fail(err)
	}
	for i, c := range r.comps {
		if err := ctx.Err(); err != nil {
			return nil, r.fail(err)
		}
		if err := r.component(ctx, i, c); err != nil {
			return nil, r.fail(err)
		}
		if err := r.save(); err != nil {
			return nil, r.fail(err)
		}
	}
	if err := r.writeConfig(); err != nil {
		return nil, r.fail(err)
	}
	if err := r.save(); err != nil {
		return nil, r.fail(err)
	}
	if err := r.makeShortcuts(scope); err != nil {
		return nil, r.fail(err)
	}
	if err := r.save(); err != nil {
		return nil, r.fail(err)
	}
	r.emit(Event{Phase: PhaseDone, Index: len(r.comps), Count: len(r.comps)})
	return &Result{Record: r.rec, Skipped: r.skipped, Shortcuts: r.shortcuts, ShortcutNotes: r.notes}, nil
}

func (r *runner) emit(e Event) {
	if r.opt.Events != nil {
		r.opt.Events(e)
	}
}

func (r *runner) logf(format string, a ...any) {
	if r.opt.Log != nil {
		r.opt.Log(fmt.Sprintf(format, a...))
	}
}

func (r *runner) save() error {
	if r.opt.RecordPath == "" {
		return nil
	}
	return r.rec.Save(r.opt.RecordPath)
}

// track records an entry created by this run.
func (r *runner) track(e record.Entry) error {
	if err := r.rec.Add(e); err != nil {
		return err
	}
	r.created = append(r.created, e)
	return nil
}

// untrack forgets an entry (a partial file that has been renamed or removed).
func (r *runner) untrack(path string) {
	path = filepath.Clean(path)
	r.rec.Remove(path)
	for i, e := range r.created {
		if e.Path == path {
			r.created = append(r.created[:i], r.created[i+1:]...)
			return
		}
	}
}

// prepare creates the chosen folders (and any missing parents), recording what
// it created as the installer's and what already existed as adopted.
func (r *runner) prepare() error {
	for _, l := range r.m.Locations {
		path := r.paths[l.ID]
		r.emit(Event{Phase: PhasePrepare, Label: l.Label, File: path})
		top, err := r.makeDirs(l.ID, path, true)
		if err != nil {
			return fmt.Errorf("preparing %s: %w", path, err)
		}
		r.rec.Locations[l.ID] = top
	}
	return nil
}

// makeDirs makes dir and every missing parent. It returns the highest folder
// it made, or dir itself when it already existed. Folders made are recorded as
// the installer's; an existing dir is recorded as adopted when asRoot is set.
// No existing folder below the location root may be a symbolic link.
func (r *runner) makeDirs(locID, dir string, asRoot bool) (string, error) {
	dir = filepath.Clean(dir)
	var missing []string
	p := dir
	for {
		_, err := os.Lstat(p)
		if err == nil {
			if isBelow(r.paths[locID], p) {
				if err := noSymlinks(r.paths[locID], p); err != nil {
					return "", err
				}
			}
			resolved, serr := os.Stat(p)
			if serr != nil {
				return "", serr
			}
			if !resolved.IsDir() {
				return "", fmt.Errorf("%s exists and is not a folder", p)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("no part of %s exists", dir)
		}
		p = parent
	}
	if len(missing) == 0 {
		if asRoot {
			if _, ok := r.rec.Locations[locID]; !ok {
				r.rec.Locations[locID] = dir
			}
			if err := r.rec.Add(record.Entry{Path: dir, Kind: record.KindDir, Owner: record.OwnerAdopted, Location: locID}); err != nil {
				return "", err
			}
		}
		return dir, nil
	}
	top := missing[len(missing)-1]
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil {
			return "", err
		}
		// The location root must be known before an entry is validated.
		if _, ok := r.rec.Locations[locID]; !ok {
			r.rec.Locations[locID] = top
		}
		if err := r.track(record.Entry{Path: missing[i], Kind: record.KindDir, Owner: record.OwnerInstaller, Location: locID}); err != nil {
			return "", err
		}
	}
	return top, nil
}

// noSymlinks checks that no part of the way from root (excluded) down to p
// (included) is a symbolic link.
func noSymlinks(root, p string) error {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return err
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; refusing to write through it", cur)
		}
	}
	return nil
}

func isBelow(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// component installs one component.
func (r *runner) component(ctx context.Context, idx int, c manifest.Component) error {
	base := r.paths[c.Location]
	if c.Dest != "" {
		rel, err := safepath.CleanRel(c.Dest)
		if err != nil {
			return fmt.Errorf("component %q: %w", c.ID, err)
		}
		base = filepath.Join(base, rel)
		if _, err := r.makeDirs(c.Location, base, false); err != nil {
			return fmt.Errorf("component %q: %w", c.ID, err)
		}
	}
	switch c.Kind {
	case manifest.ComponentZip:
		return r.installZip(ctx, idx, c, base)
	default:
		return r.installFile(ctx, idx, c, base)
	}
}

func (r *runner) event(idx int, c manifest.Component, phase Phase, file string, done, total int64) {
	r.emit(Event{Phase: phase, Component: c.ID, Label: c.Label, File: file, Done: done, Total: total, Index: idx + 1, Count: len(r.comps)})
}

func (r *runner) installFile(ctx context.Context, idx int, c manifest.Component, base string) error {
	dest := filepath.Join(base, c.Name)
	if info, err := os.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s exists and is not a regular file; not touching it", dest)
		}
		sum, err := fileSHA256(dest)
		if err != nil {
			return err
		}
		if sum != c.SHA256 {
			return fmt.Errorf("a different file named %s is already in %s; not overwriting it", c.Name, base)
		}
		r.logf("%s is already in place with the right sha256; not downloading it", dest)
		r.skipped = append(r.skipped, c.ID)
		return r.rec.Add(record.Entry{Path: dest, Kind: record.KindFile, Owner: record.OwnerAdopted, Size: info.Size(), Location: c.Location, Origin: c.ID})
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	part := filepath.Join(base, ".stellatinus-"+c.ID+".part")
	if err := r.track(record.Entry{Path: part, Kind: record.KindFile, Owner: record.OwnerInstaller, Location: c.Location, Origin: c.ID}); err != nil {
		return err
	}
	if err := r.save(); err != nil {
		return err
	}
	if err := r.fetch(ctx, idx, c, part); err != nil {
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	r.untrack(part)
	r.logf("wrote %s", dest)
	return r.track(record.Entry{Path: dest, Kind: record.KindFile, Owner: record.OwnerInstaller, Size: c.Size, Location: c.Location, Origin: c.ID})
}

// fail removes what this run created and returns the error to report.
func (r *runner) fail(cause error) error {
	items := make([]record.Item, 0, len(r.created))
	for i := len(r.created) - 1; i >= 0; i-- {
		items = append(items, record.Item{Entry: r.created[i], Action: record.ActionRemove, Reason: "created by this run"})
	}
	rep := r.rec.Apply(items)
	for _, p := range rep.Removed {
		r.rec.Remove(p)
	}
	e := &Error{Err: cause, RolledBack: len(rep.Failed) == 0, Failures: rep.Failed}
	if r.opt.RecordPath == "" {
		return e
	}
	if e.RolledBack {
		// Nothing of ours is left; an adopted folder is not worth a record.
		_ = os.Remove(r.opt.RecordPath)
		_ = os.Remove(filepath.Dir(r.opt.RecordPath)) // only if empty
	} else {
		_ = r.rec.Save(r.opt.RecordPath) // so an uninstall can finish the job
	}
	return e
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// resolveAnswers fills in defaults and refuses a value that is not valid for
// its option, or an answer for an option the manifest does not have.
func resolveAnswers(m *manifest.Manifest, given map[string]int) (map[string]int, error) {
	out := map[string]int{}
	known := map[string]bool{}
	for _, o := range m.Options {
		known[o.ID] = true
		v, ok := given[o.ID]
		if !ok {
			v = o.Default
		}
		if o.Kind == manifest.OptionPort && (v < 1024 || v > 65535) {
			return nil, fmt.Errorf("%s: %d is not a port from 1024 to 65535", o.Label, v)
		}
		out[o.ID] = v
	}
	for id := range given {
		if !known[id] {
			return nil, fmt.Errorf("there is no option %q", id)
		}
	}
	return out, nil
}

// writeConfig writes the small files the manifest asks for from the answers.
// A file that already exists is never overwritten.
func (r *runner) writeConfig() error {
	for _, cf := range r.m.Config {
		root := r.paths[cf.Location]
		dest := filepath.Join(root, cf.Name)
		r.emit(Event{Phase: PhaseConfig, File: dest})
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("%s already exists; not overwriting it", dest)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		obj := map[string]any{}
		for key, opt := range cf.Values {
			obj[key] = r.answers[opt]
		}
		data, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return err
		}
		tmp := dest + ".stellatinus-tmp"
		if err := r.track(record.Entry{Path: tmp, Kind: record.KindFile, Owner: record.OwnerInstaller, Location: cf.Location, Origin: "config"}); err != nil {
			return err
		}
		if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, dest); err != nil {
			return err
		}
		r.untrack(tmp)
		if err := r.track(record.Entry{Path: dest, Kind: record.KindFile, Owner: record.OwnerInstaller, Size: int64(len(data) + 1), Location: cf.Location, Origin: "config"}); err != nil {
			return err
		}
		r.logf("wrote %s", dest)
	}
	return nil
}
