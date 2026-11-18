// Package manifest defines the file an app gives the installer: who the app
// is, which folders it uses, what to download and which shortcuts to offer. It
// reads a manifest strictly (unknown fields are errors) and validates it. It
// does not install anything and does not touch the disk except to read the
// manifest file.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/Stellatinus/Stellatinus-Installer/internal/safepath"
	"github.com/Stellatinus/Stellatinus-Installer/internal/theme"
)

// SchemaVersion is the manifest format this code reads.
const SchemaVersion = 1

// maxSize bounds what Parse reads, so a hostile file cannot exhaust memory.
const maxSize = 1 << 20

// Channels.
const (
	ChannelDirect = "direct"
	ChannelStore  = "store"
)

// Install scopes.
const (
	ScopeUser    = "user"
	ScopeMachine = "machine"
)

// Location kinds.
const (
	KindInstall = "install"
	KindModels  = "models"
	KindOutput  = "output"
	KindData    = "data"
)

// Component kinds: a single file, or a zip archive that is unpacked.
const (
	ComponentFile = "file"
	ComponentZip  = "zip"
)

// What to do with a location when the app is removed.
const (
	OnUninstallRemove = "remove"
	OnUninstallKeep   = "keep"
	OnUninstallAsk    = "ask"
)

// Manifest is the whole file.
type Manifest struct {
	SchemaVersion int         `json:"schema_version"`
	App           App         `json:"app"`
	Channel       string      `json:"channel,omitempty"`
	Scope         string      `json:"scope,omitempty"`
	AllowMachine  bool        `json:"allow_machine,omitempty"`
	Locations     []Location  `json:"locations"`
	Components    []Component `json:"components,omitempty"`
	Shortcuts     Shortcuts   `json:"shortcuts,omitempty"`
}

// App identifies the app and sets its look.
type App struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher,omitempty"`
	// Accent is one #rrggbb colour; the installer derives every other colour
	// from it (see package theme).
	Accent string `json:"accent"`
	Logo   string `json:"logo,omitempty"`
}

// Location is a folder the user can choose.
type Location struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// Default is the proposed path per system ("windows", "linux", "darwin").
	// ${VAR} is replaced with an environment variable.
	Default map[string]string `json:"default"`
	// Required locations cannot be left out.
	Required bool `json:"required,omitempty"`
	// Adopt lets the user point at a folder that already holds content; the
	// installer then uses it where it is and never deletes it.
	Adopt       bool   `json:"adopt,omitempty"`
	OnUninstall string `json:"on_uninstall,omitempty"`
}

// Component is something to download during installation.
type Component struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
	// SHA256 and Size describe the downloaded file. Size must be right: it caps
	// the download and is used for the free-space check.
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Location string `json:"location"`
	// Kind is "file" (default: stored as one file) or "zip" (unpacked).
	Kind string `json:"kind,omitempty"`
	// Name is the file name for kind "file"; by default the last part of the URL.
	Name string `json:"name,omitempty"`
	// Dest is a sub-folder of the location, written with "/"; empty means the
	// location itself.
	Dest string `json:"dest,omitempty"`
}

// Shortcut is one place a shortcut can be offered.
type Shortcut struct {
	Offer   bool `json:"offer,omitempty"`
	Default bool `json:"default,omitempty"`
}

// Shortcuts lists the places the wizard can offer a shortcut. The taskbar is
// best effort: see docs/decisions.md.
type Shortcuts struct {
	StartMenu Shortcut `json:"start_menu,omitempty"`
	Taskbar   Shortcut `json:"taskbar,omitempty"`
	Desktop   Shortcut `json:"desktop,omitempty"`
}

// Error lists everything wrong with a manifest.
type Error struct{ Issues []string }

func (e *Error) Error() string {
	return fmt.Sprintf("invalid manifest: %d problem(s): %s", len(e.Issues), strings.Join(e.Issues, "; "))
}

var (
	idRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	locIDRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	shaRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Parse reads and validates a manifest.
func Parse(r io.Reader) (*Manifest, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("manifest is larger than %d bytes", maxSize)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("reading manifest: unexpected data after the JSON document")
	}
	m.applyDefaults()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Load reads and validates the manifest file at path.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

func (m *Manifest) applyDefaults() {
	if m.Channel == "" {
		m.Channel = ChannelDirect
	}
	if m.Scope == "" {
		m.Scope = ScopeUser
	}
	for i := range m.Locations {
		if m.Locations[i].OnUninstall == "" {
			m.Locations[i].OnUninstall = OnUninstallAsk
		}
	}
	for i := range m.Components {
		c := &m.Components[i]
		if c.Kind == "" {
			c.Kind = ComponentFile
		}
		if c.Kind == ComponentFile && c.Name == "" {
			if u, err := url.Parse(c.URL); err == nil {
				c.Name = path.Base(u.Path)
			}
		}
	}
}

// Validate checks every rule and reports all problems together.
func (m *Manifest) Validate() error {
	var iss []string
	add := func(format string, a ...any) { iss = append(iss, fmt.Sprintf(format, a...)) }

	if m.SchemaVersion != SchemaVersion {
		add("schema_version must be %d, got %d", SchemaVersion, m.SchemaVersion)
	}
	if !idRe.MatchString(m.App.ID) {
		add("app.id %q must be 2 to 32 characters: a lower-case letter, then lower-case letters, digits or '-'", m.App.ID)
	}
	if strings.TrimSpace(m.App.Name) == "" {
		add("app.name is required")
	}
	if !versionRe.MatchString(m.App.Version) {
		add("app.version %q must be a semantic version such as 1.4.0", m.App.Version)
	}
	for _, dark := range []bool{false, true} {
		if _, err := theme.Derive(m.App.Accent, dark); err != nil {
			add("app.accent: %v", err)
			break
		}
	}
	if m.Channel != ChannelDirect && m.Channel != ChannelStore {
		add("channel %q must be %q or %q", m.Channel, ChannelDirect, ChannelStore)
	}
	if m.Scope != ScopeUser && m.Scope != ScopeMachine {
		add("scope %q must be %q or %q", m.Scope, ScopeUser, ScopeMachine)
	}

	ids := map[string]bool{}
	installs := 0
	for i, l := range m.Locations {
		p := fmt.Sprintf("locations[%d]", i)
		if !locIDRe.MatchString(l.ID) {
			add("%s.id %q must start with a lower-case letter and use lower-case letters, digits or '_'", p, l.ID)
		}
		if ids[l.ID] {
			add("%s.id %q is used twice", p, l.ID)
		}
		ids[l.ID] = true
		if strings.TrimSpace(l.Label) == "" {
			add("%s.label is required", p)
		}
		switch l.Kind {
		case KindInstall:
			installs++
			if l.Adopt {
				add("%s: the install location cannot adopt an existing folder", p)
			}
		case KindModels, KindOutput, KindData:
		default:
			add("%s.kind %q must be install, models, output or data", p, l.Kind)
		}
		switch l.OnUninstall {
		case OnUninstallRemove, OnUninstallKeep, OnUninstallAsk:
		default:
			add("%s.on_uninstall %q must be remove, keep or ask", p, l.OnUninstall)
		}
		if len(l.Default) == 0 {
			add("%s.default needs a path for at least one system", p)
		}
		for sys, path := range l.Default {
			if sys != "windows" && sys != "linux" && sys != "darwin" {
				add("%s.default has an unknown system %q", p, sys)
			}
			if strings.TrimSpace(path) == "" {
				add("%s.default.%s is empty", p, sys)
			}
			for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
				if part == ".." {
					add("%s.default.%s must not contain '..'", p, sys)
					break
				}
			}
		}
	}
	if installs != 1 {
		add("exactly one location must have kind \"install\", found %d", installs)
	}

	cids := map[string]bool{}
	for i, c := range m.Components {
		p := fmt.Sprintf("components[%d]", i)
		if !locIDRe.MatchString(c.ID) {
			add("%s.id %q is not valid", p, c.ID)
		}
		if cids[c.ID] {
			add("%s.id %q is used twice", p, c.ID)
		}
		cids[c.ID] = true
		if strings.TrimSpace(c.Label) == "" {
			add("%s.label is required: the progress bar shows it", p)
		}
		if !strings.HasPrefix(c.URL, "https://") {
			add("%s.url %q must start with https://", p, c.URL)
		}
		if !shaRe.MatchString(c.SHA256) {
			add("%s.sha256 must be 64 lower-case hexadecimal digits", p)
		}
		if c.Size <= 0 {
			add("%s.size must be the size of the download in bytes, more than zero", p)
		}
		switch c.Kind {
		case ComponentFile:
			if err := safepath.ValidName(c.Name); err != nil {
				add("%s.name: %v", p, err)
			}
		case ComponentZip:
			if c.Name != "" {
				add("%s.name is only for kind %q", p, ComponentFile)
			}
		default:
			add("%s.kind %q must be %q or %q", p, c.Kind, ComponentFile, ComponentZip)
		}
		if c.Dest != "" {
			if _, err := safepath.CleanRel(c.Dest); err != nil {
				add("%s.dest: %v", p, err)
			}
		}
		if !ids[c.Location] {
			add("%s.location %q is not one of the locations", p, c.Location)
		}
	}

	if len(iss) > 0 {
		return &Error{Issues: iss}
	}
	return nil
}

// Location returns the location with the given id.
func (m *Manifest) Location(id string) (Location, bool) {
	for _, l := range m.Locations {
		if l.ID == id {
			return l, true
		}
	}
	return Location{}, false
}

// NeedBytes returns how many bytes the components stored in a location need.
func (m *Manifest) NeedBytes(locationID string) int64 {
	var n int64
	for _, c := range m.Components {
		if c.Location == locationID {
			n += c.Size
		}
	}
	return n
}

// DefaultPath returns the proposed path of a location on a system, with
// ${VAR} replaced using getenv. It fails when no path is given for that system
// or a variable is not set.
func (l Location) DefaultPath(goos string, getenv func(string) string) (string, error) {
	raw, ok := l.Default[goos]
	if !ok {
		return "", fmt.Errorf("location %q has no default path for %s", l.ID, goos)
	}
	var missing string
	out := os.Expand(raw, func(name string) string {
		v := getenv(name)
		if v == "" && missing == "" {
			missing = name
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("location %q: the variable %s is not set", l.ID, missing)
	}
	return out, nil
}
