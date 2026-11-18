package manifest

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Stellatinus/Stellatinus-Installer/internal/safepath"
)

// Option kinds and config file formats.
const (
	OptionPort = "port"
	ConfigJSON = "json"
)

// Option is a question the wizard asks that is not a folder. Only a port is
// supported: a number the app listens on, checked by really trying to listen.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// Default is the proposed value; for a port, from 1024 to 65535 (a lower
	// port needs privileges).
	Default int `json:"default"`
}

// ConfigFile is a small file the installer writes for the app from the
// answers, for example {"port": 5081} in the app's data folder. It is never
// written over a file that already exists.
type ConfigFile struct {
	Location string `json:"location"`
	Name     string `json:"name"`
	Format   string `json:"format"`
	// Values maps a key in the file to the id of an option.
	Values map[string]string `json:"values"`
}

var (
	langRe     = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)
	platformRe = regexp.MustCompile(`^(windows|linux|darwin)(/(amd64|arm64))?$`)
)

// AppliesTo reports whether a component is for the given system. A component
// with no platforms is for every system; "windows" means every architecture
// of Windows, "windows/amd64" one.
func (c Component) AppliesTo(goos, goarch string) bool {
	if len(c.Platforms) == 0 {
		return true
	}
	for _, p := range c.Platforms {
		if p == goos || p == goos+"/"+goarch {
			return true
		}
	}
	return false
}

// ComponentsFor returns the components to install on a system, in order.
func (m *Manifest) ComponentsFor(goos, goarch string) []Component {
	var out []Component
	for _, c := range m.Components {
		if c.AppliesTo(goos, goarch) {
			out = append(out, c)
		}
	}
	return out
}

// NeedBytesFor returns how many bytes the components stored in a location need
// on a system.
func (m *Manifest) NeedBytesFor(locationID, goos, goarch string) int64 {
	var n int64
	for _, c := range m.ComponentsFor(goos, goarch) {
		if c.Location == locationID {
			n += c.Size
		}
	}
	return n
}

// textKeys lists every text that can be translated, with its English text.
func (m *Manifest) textKeys() map[string]string {
	k := map[string]string{"app.name": m.App.Name}
	if m.App.Description != "" {
		k["app.description"] = m.App.Description
	}
	for _, l := range m.Locations {
		k["location."+l.ID+".label"] = l.Label
	}
	for _, c := range m.Components {
		k["component."+c.ID+".label"] = c.Label
	}
	for _, o := range m.Options {
		k["option."+o.ID+".label"] = o.Label
	}
	return k
}

// Text returns the text for a key in a language: the translation if there is
// one (for "pt-BR" also "pt"), else the English text from the manifest's own
// fields, else "".
func (m *Manifest) Text(lang, key string) string {
	if lang != "" && lang != "en" {
		tries := []string{lang}
		if base, _, ok := strings.Cut(lang, "-"); ok {
			tries = append(tries, base)
		}
		for _, l := range tries {
			if t, ok := m.Texts[l][key]; ok {
				return t
			}
		}
	}
	return m.textKeys()[key]
}

// MissingTexts lists the keys that have an English text but no translation in
// lang. The projects' own apps must have none for "it".
func (m *Manifest) MissingTexts(lang string) []string {
	var out []string
	for key := range m.textKeys() {
		if _, ok := m.Texts[lang][key]; !ok {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func validText(s string) bool {
	if s == "" || len(s) > 300 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// validateExtras checks options, config files, platforms and texts.
func (m *Manifest) validateExtras(add func(string, ...any), locIDs, compIDs map[string]bool) {
	for i, c := range m.Components {
		for _, p := range c.Platforms {
			if !platformRe.MatchString(p) {
				add("components[%d].platforms: %q must be windows, linux or darwin, optionally followed by /amd64 or /arm64", i, p)
			}
		}
	}

	optIDs := map[string]bool{}
	for i, o := range m.Options {
		p := fmt.Sprintf("options[%d]", i)
		if !locIDRe.MatchString(o.ID) {
			add("%s.id %q must start with a lower-case letter and use lower-case letters, digits or '_'", p, o.ID)
		}
		if optIDs[o.ID] {
			add("%s.id %q is used twice", p, o.ID)
		}
		optIDs[o.ID] = true
		if strings.TrimSpace(o.Label) == "" {
			add("%s.label is required", p)
		}
		if o.Kind != OptionPort {
			add("%s.kind %q must be %q", p, o.Kind, OptionPort)
		} else if o.Default < 1024 || o.Default > 65535 {
			add("%s.default %d must be a port from 1024 to 65535 (a lower one needs privileges)", p, o.Default)
		}
	}

	seen := map[string]bool{}
	for i, cf := range m.Config {
		p := fmt.Sprintf("config[%d]", i)
		if !locIDs[cf.Location] {
			add("%s.location %q is not one of the locations", p, cf.Location)
		}
		if err := safepath.ValidName(cf.Name); err != nil {
			add("%s.name: %v", p, err)
		}
		if cf.Format != ConfigJSON {
			add("%s.format %q must be %q", p, cf.Format, ConfigJSON)
		}
		if len(cf.Values) == 0 {
			add("%s.values needs at least one key", p)
		}
		for key, opt := range cf.Values {
			if key == "" || len(key) > 64 || !validText(key) {
				add("%s.values has an invalid key %q", p, key)
			}
			if !optIDs[opt] {
				add("%s.values[%q] names %q, which is not an option", p, key, opt)
			}
		}
		id := cf.Location + "/" + strings.ToLower(cf.Name)
		if seen[id] {
			add("%s writes %s/%s twice", p, cf.Location, cf.Name)
		}
		seen[id] = true
	}

	keys := m.textKeys()
	for lang, texts := range m.Texts {
		if lang == "en" {
			add("texts.en: English is the base text, written in the fields themselves")
			continue
		}
		if !langRe.MatchString(lang) {
			add("texts: %q is not a language code such as it or pt-BR", lang)
			continue
		}
		for key, val := range texts {
			if _, ok := keys[key]; !ok {
				add("texts.%s: %q does not name a text (use app.name, app.description, location.<id>.label, component.<id>.label or option.<id>.label)", lang, key)
			}
			if !validText(val) {
				add("texts.%s.%s must be 1 to 300 characters without control characters", lang, key)
			}
		}
	}
}
