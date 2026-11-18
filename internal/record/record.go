// Package record keeps the installation record: every file, folder and
// shortcut the installer created or adopted, and who owns each. Removal
// follows the record and nothing else: it deletes only what the installer
// created, never what the user adopted or added, and it never follows symbolic
// links out of a recorded folder. It does not decide what the user wants to
// keep (the window passes Choices) and it does not install anything.
package record

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/safepath"
)

// Version is the record format this code reads and writes.
const Version = 1

const maxSize = 64 << 20

// Owner says who created an entry, and so who may delete it.
type Owner string

// Owners.
const (
	OwnerInstaller Owner = "installer" // created by the installer: may be removed
	OwnerAdopted   Owner = "adopted"   // already there when the user pointed at it: never removed
	OwnerUser      Owner = "user"      // added later by the user or by the app: never removed
)

// Kind is what an entry is.
type Kind string

// Kinds.
const (
	KindFile     Kind = "file"
	KindDir      Kind = "dir"
	KindShortcut Kind = "shortcut"
)

// Entry is one thing the installer knows about.
type Entry struct {
	Path     string `json:"path"`
	Kind     Kind   `json:"kind"`
	Owner    Owner  `json:"owner"`
	Size     int64  `json:"size,omitempty"`
	Location string `json:"location"`         // the manifest location it belongs to
	Origin   string `json:"origin,omitempty"` // the component that produced it
}

// Record is the whole file.
type Record struct {
	Version     int               `json:"version"`
	AppID       string            `json:"app_id"`
	AppVersion  string            `json:"app_version"`
	Scope       string            `json:"scope"`
	InstalledAt time.Time         `json:"installed_at"`
	Locations   map[string]string `json:"locations"` // location id to the folder the user chose
	Entries     []Entry           `json:"entries"`
}

// New starts an empty record.
func New(appID, appVersion, scope string, locations map[string]string) *Record {
	cp := make(map[string]string, len(locations))
	for k, v := range locations {
		cp[k] = filepath.Clean(v)
	}
	return &Record{Version: Version, AppID: appID, AppVersion: appVersion, Scope: scope, InstalledAt: time.Now().UTC(), Locations: cp}
}

func (r *Record) validate(e Entry) error {
	if !filepath.IsAbs(e.Path) {
		return fmt.Errorf("record: %q is not an absolute path", e.Path)
	}
	switch e.Kind {
	case KindFile, KindDir, KindShortcut:
	default:
		return fmt.Errorf("record: %q has an unknown kind %q", e.Path, e.Kind)
	}
	switch e.Owner {
	case OwnerInstaller, OwnerAdopted, OwnerUser:
	default:
		return fmt.Errorf("record: %q has an unknown owner %q", e.Path, e.Owner)
	}
	if _, ok := r.Locations[e.Location]; !ok && e.Kind != KindShortcut {
		return fmt.Errorf("record: %q belongs to an unknown location %q", e.Path, e.Location)
	}
	return nil
}

// Add records an entry, replacing an earlier one for the same path.
func (r *Record) Add(e Entry) error {
	e.Path = filepath.Clean(e.Path)
	if err := r.validate(e); err != nil {
		return err
	}
	for i := range r.Entries {
		if r.Entries[i].Path == e.Path {
			r.Entries[i] = e
			return nil
		}
	}
	r.Entries = append(r.Entries, e)
	return nil
}

// DefaultPath is where the record of an installation lives, so install and
// uninstall find it from the app id and the scope alone. It is outside every
// folder the user chooses, so removing an installation never removes its own
// record first. For a user it is under the user's configuration folder; for
// everybody under the machine-wide data folder.
func DefaultPath(appID, scope string) (string, error) {
	if err := safepath.ValidName(appID); err != nil {
		return "", fmt.Errorf("record: the app id: %w", err)
	}
	var base string
	switch scope {
	case "machine":
		switch runtime.GOOS {
		case "windows":
			base = os.Getenv("ProgramData")
		case "darwin":
			base = "/Library/Application Support"
		default:
			base = "/var/lib"
		}
		if base == "" {
			return "", errors.New("record: the machine-wide data folder is not known on this system")
		}
	case "user", "":
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("record: the user's configuration folder: %w", err)
		}
		base = dir
	default:
		return "", fmt.Errorf("record: unknown scope %q", scope)
	}
	return filepath.Join(base, "Stellatinus", appID, "record.json"), nil
}

// Owner returns who owns the entry recorded for path, if there is one.
func (r *Record) Owner(path string) (Owner, bool) {
	path = filepath.Clean(path)
	for _, e := range r.Entries {
		if e.Path == path {
			return e.Owner, true
		}
	}
	return "", false
}

// Remove drops the entry recorded for path and reports whether there was one.
// It does not touch the disk.
func (r *Record) Remove(path string) bool {
	path = filepath.Clean(path)
	for i, e := range r.Entries {
		if e.Path == path {
			r.Entries = append(r.Entries[:i], r.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// Save writes the record atomically: a temporary file in the same folder is
// written and flushed, then renamed over the old one, so a crash leaves either
// the old record or the new one.
func (r *Record) Save(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".record-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil { // best effort: not every system can sync a folder
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Load reads and checks a record.
func Load(path string) (*Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize {
		return nil, errors.New("record: file is too large")
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}
	if r.Version != Version {
		return nil, fmt.Errorf("record: version %d is not supported (this program reads version %d)", r.Version, Version)
	}
	if r.Locations == nil {
		r.Locations = map[string]string{}
	}
	for _, e := range r.Entries {
		if err := r.validate(e); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// Action is what removal will do with an entry.
type Action string

// Actions.
const (
	ActionRemove    Action = "remove"
	ActionKeep      Action = "keep"      // the user chose to keep it
	ActionProtected Action = "protected" // not the installer's: never removed
)

// Item is one line of the removal plan.
type Item struct {
	Entry
	Action Action `json:"action"`
	Reason string `json:"reason"`
}

// Choices are what the user decided on the "what to keep" screen.
type Choices struct {
	KeepLocation map[string]bool // keep everything the installer made in this location
	KeepPath     map[string]bool // keep this one entry
}

// Plan says what removal would do, without touching the disk. Entries the
// installer does not own are always protected, whatever the choices.
func (r *Record) Plan(c Choices) []Item {
	items := make([]Item, 0, len(r.Entries))
	for _, e := range r.Entries {
		it := Item{Entry: e}
		switch {
		case e.Owner != OwnerInstaller:
			it.Action, it.Reason = ActionProtected, "not created by the installer"
		case c.KeepPath[e.Path]:
			it.Action, it.Reason = ActionKeep, "you chose to keep it"
		case c.KeepLocation[e.Location]:
			it.Action, it.Reason = ActionKeep, "you chose to keep this folder's content"
		default:
			it.Action, it.Reason = ActionRemove, "created by the installer"
		}
		items = append(items, it)
	}
	return items
}

// Totals sums sizes by action, for the "space freed" line.
func Totals(items []Item) (remove, keep, protected int64) {
	for _, it := range items {
		switch it.Action {
		case ActionRemove:
			remove += it.Size
		case ActionKeep:
			keep += it.Size
		case ActionProtected:
			protected += it.Size
		}
	}
	return
}

// Failure is something that could not be removed, and why.
type Failure struct {
	Path string
	Err  error
}

// Report is what Apply did.
type Report struct {
	Removed   []string
	Kept      []string
	Protected []string
	Failed    []Failure
}

// Apply carries out a plan. Files and shortcuts go first, then folders from
// the deepest up; a folder that is not empty is left, because it holds
// something the installer did not create. Every path is checked against its
// location: it must be inside it and no folder on the way may be a symbolic
// link, so a link can never lead a removal outside the install.
func (r *Record) Apply(items []Item) Report {
	var rep Report
	var files, dirs []Item
	for _, it := range items {
		switch it.Action {
		case ActionKeep:
			rep.Kept = append(rep.Kept, it.Path)
		case ActionProtected:
			rep.Protected = append(rep.Protected, it.Path)
		case ActionRemove:
			if it.Kind == KindDir {
				dirs = append(dirs, it)
			} else {
				files = append(files, it)
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].Path) > len(dirs[j].Path) })
	for _, it := range append(files, dirs...) {
		if err := r.remove(it); err != nil {
			rep.Failed = append(rep.Failed, Failure{Path: it.Path, Err: err})
			continue
		}
		rep.Removed = append(rep.Removed, it.Path)
	}
	return rep
}

func (r *Record) remove(it Item) error {
	if it.Kind != KindShortcut {
		root, ok := r.Locations[it.Location]
		if !ok {
			return fmt.Errorf("unknown location %q", it.Location)
		}
		if err := checkInside(root, it.Path, it.Kind == KindDir); err != nil {
			return err
		}
	}
	info, err := os.Lstat(it.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // already gone
	}
	if err != nil {
		return err
	}
	if it.Kind == KindDir {
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("recorded as a folder but it is not one; left alone")
		}
		if err := os.Remove(it.Path); err != nil {
			if entries, rerr := os.ReadDir(it.Path); rerr == nil && len(entries) > 0 {
				return errors.New("folder is not empty: it holds files the installer did not create")
			}
			return err
		}
		return nil
	}
	if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		return errors.New("recorded as a file but it is a folder; left alone")
	}
	return os.Remove(it.Path) // a symbolic link is removed itself, its target is untouched
}

// checkInside verifies path is below root (or is root itself, but only when it
// is a folder) and that no part of the way from root to the entry's parent is
// a symbolic link.
func checkInside(root, path string, isDir bool) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || (rel == "." && !isDir) {
		return fmt.Errorf("%s is outside %s; refusing to remove it", path, root)
	}
	if rel == "." {
		return nil
	}
	cur := root
	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	for _, p := range parts {
		if p == "." || p == "" {
			continue
		}
		cur = filepath.Join(cur, p)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; refusing to remove through it", cur)
		}
	}
	return nil
}
