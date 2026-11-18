// Package checks runs the verifications the wizard shows one by one before it
// installs anything: privileges, permission to write in each folder (by really
// creating and deleting a test file), free space, folders that overlap, and an
// existing installation. Every check returns a Result the window can show,
// with the step to go back to when it fails. It never creates folders, never
// keeps the test files and never installs anything.
package checks

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
)

// Status is the outcome of one check.
type Status string

// The outcomes. A Warning does not stop the installation.
const (
	OK      Status = "ok"
	Failed  Status = "failed"
	Warning Status = "warning"
)

// What the window should offer to fix a failed check.
const (
	FixElevate      = "elevate"       // restart as administrator
	FixChooseFolder = "choose-folder" // go back to the step of Result.GoBack
	FixChooseOption = "choose-option" // go back to the question "option:<id>" in Result.GoBack
)

// Result is one line in the checks screen.
type Result struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	// Fix is what the window offers when Status is Failed; GoBack is the
	// location id of the step it returns to for FixChooseFolder.
	Fix    string `json:"fix,omitempty"`
	GoBack string `json:"go_back,omitempty"`
}

// Passed reports whether the check does not block the installation.
func (r Result) Passed() bool { return r.Status != Failed }

// Privileges checks that the installer runs with the rights the scope needs.
// A per-user install needs none; a per-machine install needs an elevated
// process.
func Privileges(scope string, elevated bool) Result {
	r := Result{ID: "privileges", Title: "Sufficient privileges", Status: OK}
	switch {
	case scope == manifest.ScopeMachine && !elevated:
		r.Title, r.Status = "Insufficient privileges", Failed
		r.Detail = "Installing for all users needs administrator rights, and this program was not started with them."
		r.Fix = FixElevate
	case scope == manifest.ScopeMachine:
		r.Detail = "Started with administrator rights, as installing for all users needs."
	default:
		r.Detail = "Installing for the current user: no administrator rights needed."
	}
	return r
}

// nearestExisting returns dir, or its closest parent that exists.
func nearestExisting(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%q is not an absolute path", dir)
	}
	p := filepath.Clean(dir)
	for {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("no part of %q exists", dir)
		}
		p = parent
	}
}

func writeFailure(loc string, detail string) Result {
	return Result{ID: "write:" + loc, Title: "No permission to write", Status: Failed, Detail: detail, Fix: FixChooseFolder, GoBack: loc}
}

// WriteAccess checks that files can be created in dir (or, if dir does not
// exist yet, in the closest existing parent, where the folder would be
// created). It creates a test file and deletes it; it never creates dir. This
// is needed even when running as administrator: a folder can be read-only, on
// a network drive, deny the user, or be protected by the system.
func WriteAccess(loc, dir string) Result {
	probe, err := nearestExisting(dir)
	if err != nil {
		return writeFailure(loc, err.Error())
	}
	info, err := os.Stat(probe)
	if err != nil {
		return writeFailure(loc, err.Error())
	}
	if !info.IsDir() {
		return writeFailure(loc, fmt.Sprintf("%s is a file, not a folder.", probe))
	}
	f, err := os.CreateTemp(probe, ".stellatinus-write-test-*")
	if err != nil {
		return writeFailure(loc, fmt.Sprintf("Cannot create a file in %s: %s.", probe, reason(err)))
	}
	name := f.Name()
	_, werr := f.WriteString("test")
	cerr := f.Close()
	rerr := os.Remove(name)
	if err := errors.Join(werr, cerr, rerr); err != nil {
		return writeFailure(loc, fmt.Sprintf("The test file in %s could not be written and removed: %s.", probe, reason(err)))
	}
	return Result{ID: "write:" + loc, Title: "Permission to write in the folder", Status: OK,
		Detail: fmt.Sprintf("Created and removed a test file in %s.", probe)}
}

// ReadAccess checks that an existing folder can be listed. It is used for a
// folder the installer only reads from, such as adopted models.
func ReadAccess(loc, dir string) Result {
	if _, err := os.ReadDir(dir); err != nil {
		return Result{ID: "read:" + loc, Title: "Cannot read the folder", Status: Failed,
			Detail: fmt.Sprintf("Cannot read %s: %s.", dir, reason(err)), Fix: FixChooseFolder, GoBack: loc}
	}
	return Result{ID: "read:" + loc, Title: "The folder can be read", Status: OK, Detail: fmt.Sprintf("%s can be read.", dir)}
}

// FreeSpace checks that the disk holding dir has at least need bytes free.
func FreeSpace(loc, dir string, need int64) Result {
	probe, err := nearestExisting(dir)
	if err != nil {
		return Result{ID: "space:" + loc, Title: "Cannot measure free space", Status: Failed, Detail: err.Error(), Fix: FixChooseFolder, GoBack: loc}
	}
	free, err := freeBytes(probe)
	if err != nil {
		return Result{ID: "space:" + loc, Title: "Cannot measure free space", Status: Failed,
			Detail: fmt.Sprintf("Cannot read the free space of %s: %s.", probe, reason(err)), Fix: FixChooseFolder, GoBack: loc}
	}
	if need < 0 || uint64(need) > free {
		return Result{ID: "space:" + loc, Title: "Not enough free space", Status: Failed,
			Detail: fmt.Sprintf("%s needed, %s free on the disk of %s.", FormatBytes(need), FormatBytes(int64(min(free, 1<<62))), probe),
			Fix:    FixChooseFolder, GoBack: loc}
	}
	return Result{ID: "space:" + loc, Title: "Enough free space", Status: OK,
		Detail: fmt.Sprintf("%s needed, %s free.", FormatBytes(need), FormatBytes(int64(min(free, 1<<62))))}
}

// Overlap checks that no chosen folder is the same as, or inside, another.
// order lists the location ids in the order of the wizard steps; when two
// overlap, the later one is the step to go back to.
func Overlap(paths map[string]string, order []string) Result {
	norm := func(p string) string {
		p = filepath.Clean(p)
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			p = strings.ToLower(p)
		}
		return p
	}
	inside := func(child, parent string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	for i, a := range order {
		for _, b := range order[i+1:] {
			pa, oka := paths[a]
			pb, okb := paths[b]
			if !oka || !okb {
				continue
			}
			na, nb := norm(pa), norm(pb)
			if inside(na, nb) || inside(nb, na) {
				return Result{ID: "overlap", Title: "The folders overlap", Status: Failed,
					Detail: fmt.Sprintf("%s and %s are the same folder or one is inside the other.", pa, pb),
					Fix:    FixChooseFolder, GoBack: b}
			}
		}
	}
	return Result{ID: "overlap", Title: "The folders do not overlap", Status: OK, Detail: "Each folder is separate."}
}

// PortFree checks that a port can be listened on at 127.0.0.1, by really
// listening on it for a moment: a number that looks fine can still be taken by
// another program or refused by the system.
func PortFree(id string, port int) Result {
	r := Result{ID: "port:" + id, Title: "The port is free", Status: OK}
	fail := func(title, detail string) Result {
		return Result{ID: "port:" + id, Title: title, Status: Failed, Detail: detail, Fix: FixChooseOption, GoBack: "option:" + id}
	}
	if port < 1 || port > 65535 {
		return fail("Not a valid port", fmt.Sprintf("%d is not a port number (1 to 65535).", port))
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fail("The port is in use or not allowed", fmt.Sprintf("Port %d cannot be used: %s.", port, reason(err)))
	}
	_ = ln.Close()
	r.Detail = fmt.Sprintf("Port %d is free.", port)
	return r
}

// Existing warns when an installation record is already there.
func Existing(recordPath string) Result {
	if _, err := os.Stat(recordPath); err == nil {
		return Result{ID: "existing", Title: "Already installed", Status: Warning,
			Detail: "An earlier installation was found; installing again will repair or update it."}
	}
	return Result{ID: "existing", Title: "No earlier installation", Status: OK, Detail: "No other copy was found."}
}

// Options are what Run needs besides the manifest and the chosen folders.
type Options struct {
	Scope      string
	Elevated   bool
	RecordPath string // empty skips the existing-installation check
	// GOOS and GOARCH choose which components count (default: this system).
	GOOS, GOARCH string
	// Answers holds the value of each option by id; a missing one uses the
	// manifest's default.
	Answers map[string]int
}

// Run performs every check for a manifest and the folders the user chose
// (location id to absolute path), in the order the wizard shows them.
func Run(m *manifest.Manifest, paths map[string]string, opt Options) []Result {
	scope := opt.Scope
	if scope == "" {
		scope = m.Scope
	}
	goos, goarch := opt.GOOS, opt.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	out := []Result{Privileges(scope, opt.Elevated)}
	order := make([]string, 0, len(m.Locations))
	for _, l := range m.Locations {
		order = append(order, l.ID)
		dir, ok := paths[l.ID]
		if !ok {
			continue
		}
		need := m.NeedBytesFor(l.ID, goos, goarch)
		readOnly := l.Kind == manifest.KindModels && l.Adopt && need == 0
		if readOnly {
			if _, err := os.Stat(dir); err == nil {
				out = append(out, ReadAccess(l.ID, dir))
				continue
			}
		}
		out = append(out, WriteAccess(l.ID, dir))
		if need > 0 {
			out = append(out, FreeSpace(l.ID, dir, need))
		}
	}
	out = append(out, Overlap(paths, order))
	for _, o := range m.Options {
		if o.Kind != manifest.OptionPort {
			continue
		}
		v, ok := opt.Answers[o.ID]
		if !ok {
			v = o.Default
		}
		out = append(out, PortFree(o.ID, v))
	}
	if opt.RecordPath != "" {
		out = append(out, Existing(opt.RecordPath))
	}
	return out
}

// reason turns an error into a short sentence a person can act on.
func reason(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "it does not exist"
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return msg
}

// FormatBytes writes a size the way the wizard shows it (1,1 GB uses a dot
// here; the window formats it for the language).
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}
