package manifest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const valid = `{
  "schema_version": 1,
  "app": {"id": "haedus", "name": "Haedus", "version": "1.4.0", "accent": "#f2c200"},
  "locations": [
    {"id": "install", "label": "Program", "kind": "install", "required": true,
     "default": {"windows": "${LOCALAPPDATA}\\Haedus", "linux": "${HOME}/.local/share/haedus"}},
    {"id": "models", "label": "AI models", "kind": "models", "adopt": true, "on_uninstall": "keep",
     "default": {"windows": "${USERPROFILE}\\Haedus\\Models"}}
  ],
  "components": [
    {"id": "core", "label": "the core", "url": "https://example.invalid/core.zip",
     "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "size": 1000, "location": "install"}
  ]
}`

func TestParseValidAppliesDefaults(t *testing.T) {
	m, err := Parse(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if m.Channel != ChannelDirect || m.Scope != ScopeUser {
		t.Fatalf("defaults: channel %q scope %q", m.Channel, m.Scope)
	}
	if m.Locations[0].OnUninstall != OnUninstallAsk {
		t.Fatalf("on_uninstall default = %q", m.Locations[0].OnUninstall)
	}
	if got := m.NeedBytesFor("install", "linux", "amd64"); got != 1000 {
		t.Fatalf("NeedBytes = %d", got)
	}
}

func mutate(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(valid), &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRejects(t *testing.T) {
	cases := map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"unknown field": {func(d map[string]any) { d["surprise"] = 1 }, "unknown field"},
		"wrong schema":  {func(d map[string]any) { d["schema_version"] = 2 }, "schema_version"},
		"bad id":        {func(d map[string]any) { d["app"].(map[string]any)["id"] = "Haedus!" }, "app.id"},
		"bad version":   {func(d map[string]any) { d["app"].(map[string]any)["version"] = "1.4" }, "app.version"},
		"bad accent":    {func(d map[string]any) { d["app"].(map[string]any)["accent"] = "yellow" }, "app.accent"},
		"bad channel":   {func(d map[string]any) { d["channel"] = "beta" }, "channel"},
		"http url": {func(d map[string]any) {
			d["components"].([]any)[0].(map[string]any)["url"] = "http://example.invalid/core.zip"
		}, "https://"},
		"short sha": {func(d map[string]any) { d["components"].([]any)[0].(map[string]any)["sha256"] = "abc" }, "sha256"},
		"unknown location": {func(d map[string]any) {
			d["components"].([]any)[0].(map[string]any)["location"] = "nowhere"
		}, "not one of the locations"},
		"two installs": {func(d map[string]any) { d["locations"].([]any)[1].(map[string]any)["kind"] = "install" }, "exactly one"},
		"adopt install": {func(d map[string]any) {
			d["locations"].([]any)[0].(map[string]any)["adopt"] = true
		}, "cannot adopt"},
		"dot dot": {func(d map[string]any) {
			d["locations"].([]any)[0].(map[string]any)["default"] = map[string]any{"linux": "${HOME}/../etc"}
		}, "'..'"},
		"zero size": {func(d map[string]any) { d["components"].([]any)[0].(map[string]any)["size"] = 0 }, "more than zero"},
		"bad kind":  {func(d map[string]any) { d["components"].([]any)[0].(map[string]any)["kind"] = "tar" }, "components[0].kind"},
		"name with a slash": {func(d map[string]any) {
			d["components"].([]any)[0].(map[string]any)["name"] = "sub/core.zip"
		}, "components[0].name"},
		"reserved name": {func(d map[string]any) { d["components"].([]any)[0].(map[string]any)["name"] = "con.zip" }, "reserved"},
		"name on a zip": {func(d map[string]any) {
			c := d["components"].([]any)[0].(map[string]any)
			c["kind"], c["name"] = "zip", "x.zip"
		}, "only for kind"},
		"dest with dot dot": {func(d map[string]any) { d["components"].([]any)[0].(map[string]any)["dest"] = "a/../b" }, "components[0].dest"},
		"absolute dest":     {func(d map[string]any) { d["components"].([]any)[0].(map[string]any)["dest"] = "/etc" }, "components[0].dest"},
		"bad platform": {func(d map[string]any) {
			d["components"].([]any)[0].(map[string]any)["platforms"] = []any{"windows/x86"}
		}, "platforms"},
		"bad option kind": {func(d map[string]any) {
			d["options"] = []any{map[string]any{"id": "p", "label": "P", "kind": "text", "default": 5080}}
		}, "options[0].kind"},
		"a privileged default port": {func(d map[string]any) {
			d["options"] = []any{map[string]any{"id": "p", "label": "P", "kind": "port", "default": 80}}
		}, "1024 to 65535"},
		"the same option twice": {func(d map[string]any) {
			o := map[string]any{"id": "p", "label": "P", "kind": "port", "default": 5080}
			d["options"] = []any{o, o}
		}, "used twice"},
		"config naming no option": {func(d map[string]any) {
			d["config"] = []any{map[string]any{"location": "install", "name": "c.json", "format": "json", "values": map[string]any{"port": "nope"}}}
		}, "not an option"},
		"config in no location": {func(d map[string]any) {
			d["options"] = []any{map[string]any{"id": "p", "label": "P", "kind": "port", "default": 5080}}
			d["config"] = []any{map[string]any{"location": "nowhere", "name": "c.json", "format": "json", "values": map[string]any{"port": "p"}}}
		}, "config[0].location"},
		"config with a bad name": {func(d map[string]any) {
			d["options"] = []any{map[string]any{"id": "p", "label": "P", "kind": "port", "default": 5080}}
			d["config"] = []any{map[string]any{"location": "install", "name": "../c.json", "format": "json", "values": map[string]any{"port": "p"}}}
		}, "config[0].name"},
		"config in an unknown format": {func(d map[string]any) {
			d["options"] = []any{map[string]any{"id": "p", "label": "P", "kind": "port", "default": 5080}}
			d["config"] = []any{map[string]any{"location": "install", "name": "c.xml", "format": "xml", "values": map[string]any{"port": "p"}}}
		}, "config[0].format"},
		"English in texts": {func(d map[string]any) {
			d["texts"] = map[string]any{"en": map[string]any{"app.name": "x"}}
		}, "English is the base"},
		"a bad language code": {func(d map[string]any) {
			d["texts"] = map[string]any{"Italian": map[string]any{"app.name": "x"}}
		}, "language code"},
		"a text for nothing": {func(d map[string]any) {
			d["texts"] = map[string]any{"it": map[string]any{"location.nowhere.label": "x"}}
		}, "does not name a text"},
		"an empty text": {func(d map[string]any) {
			d["texts"] = map[string]any{"it": map[string]any{"app.name": ""}}
		}, "1 to 300 characters"},
		"duplicate location": {func(d map[string]any) { d["locations"].([]any)[1].(map[string]any)["id"] = "install" }, "used twice"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(mutate(t, tc.edit)))
			if err == nil {
				t.Fatal("accepted an invalid manifest")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestReportsAllProblemsTogether(t *testing.T) {
	doc := mutate(t, func(d map[string]any) {
		d["app"].(map[string]any)["id"] = "X"
		d["app"].(map[string]any)["version"] = "x"
	})
	_, err := Parse(strings.NewReader(doc))
	var me *Error
	if !errors.As(err, &me) || len(me.Issues) < 2 {
		t.Fatalf("want an *Error with at least 2 issues, got %v", err)
	}
}

func TestRejectsTrailingData(t *testing.T) {
	if _, err := Parse(strings.NewReader(valid + `{}`)); err == nil {
		t.Fatal("accepted data after the document")
	}
}

func TestRejectsHugeFile(t *testing.T) {
	if _, err := Parse(strings.NewReader(strings.Repeat(" ", maxSize+10))); err == nil {
		t.Fatal("accepted a file larger than the limit")
	}
}

func TestDefaultPath(t *testing.T) {
	l := Location{ID: "install", Default: map[string]string{"linux": "${HOME}/x"}}
	env := func(k string) string {
		if k == "HOME" {
			return "/home/u"
		}
		return ""
	}
	got, err := l.DefaultPath("linux", env)
	if err != nil || got != "/home/u/x" {
		t.Fatalf("DefaultPath = %q, %v", got, err)
	}
	if _, err := l.DefaultPath("windows", env); err == nil {
		t.Fatal("expected an error for a system without a path")
	}
	l.Default["linux"] = "${NOPE}/x"
	if _, err := l.DefaultPath("linux", env); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("expected an error naming the variable, got %v", err)
	}
}

// Every example in examples/ must be a valid manifest.
func TestExamplesAreValid(t *testing.T) {
	files, err := filepath.Glob("../../examples/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples found: %v", err)
	}
	for _, f := range files {
		if _, err := Load(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// The JSON Schema in schema/ must describe every field the code reads, so the
// two cannot drift apart without a test failing.
func TestSchemaListsEveryField(t *testing.T) {
	raw, err := os.ReadFile("../../schema/manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	defs, _ := schema["$defs"].(map[string]any)
	check := func(where string, props map[string]any, typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			if _, ok := props[name]; !ok {
				t.Errorf("schema %s has no property %q", where, name)
			}
		}
		for name := range props {
			found := false
			for i := 0; i < typ.NumField(); i++ {
				if strings.Split(typ.Field(i).Tag.Get("json"), ",")[0] == name {
					found = true
				}
			}
			if !found {
				t.Errorf("schema %s lists %q, which the code does not read", where, name)
			}
		}
	}
	props := func(m map[string]any) map[string]any { p, _ := m["properties"].(map[string]any); return p }
	check("root", props(schema), reflect.TypeOf(Manifest{}))
	for name, typ := range map[string]reflect.Type{
		"app": reflect.TypeOf(App{}), "location": reflect.TypeOf(Location{}), "component": reflect.TypeOf(Component{}),
		"shortcuts": reflect.TypeOf(Shortcuts{}), "shortcut": reflect.TypeOf(Shortcut{}), "launch": reflect.TypeOf(Launch{}),
		"option": reflect.TypeOf(Option{}), "config_file": reflect.TypeOf(ConfigFile{}),
	} {
		d, ok := defs[name].(map[string]any)
		if !ok {
			t.Errorf("schema has no $defs.%s", name)
			continue
		}
		check("$defs."+name, props(d), typ)
	}
}

func TestComponentDefaults(t *testing.T) {
	m, err := Parse(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	c := m.Components[0]
	if c.Kind != ComponentFile || c.Name != "core.zip" {
		t.Fatalf("defaults: kind %q name %q, want file and the last part of the URL", c.Kind, c.Name)
	}
	// A query string is not part of the name.
	doc := mutate(t, func(d map[string]any) {
		d["components"].([]any)[0].(map[string]any)["url"] = "https://example.invalid/dl/model.bin?token=abc"
	})
	m, err = Parse(strings.NewReader(doc))
	if err != nil || m.Components[0].Name != "model.bin" {
		t.Fatalf("name from a URL with a query: %v %+v", err, m)
	}
}

func TestTextFallsBackToEnglish(t *testing.T) {
	doc := mutate(t, func(d map[string]any) {
		d["app"].(map[string]any)["description"] = "Turns speech into text."
		d["texts"] = map[string]any{
			"it":    map[string]any{"app.description": "Trasforma la voce in testo.", "location.models.label": "Modelli IA"},
			"pt":    map[string]any{"app.description": "Transforma fala em texto."},
			"pt-BR": map[string]any{"location.models.label": "Modelos de IA"},
		}
	})
	m, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ lang, key, want string }{
		{"", "app.description", "Turns speech into text."},
		{"en", "app.description", "Turns speech into text."},
		{"it", "app.description", "Trasforma la voce in testo."},
		{"it", "location.models.label", "Modelli IA"},
		{"it", "location.install.label", "Program"},                 // not translated: English
		{"it-IT", "app.description", "Trasforma la voce in testo."}, // region falls back to the language
		{"pt-BR", "location.models.label", "Modelos de IA"},
		{"pt-BR", "app.description", "Transforma fala em texto."}, // pt-BR has none: falls back to pt
		{"fr", "app.description", "Turns speech into text."},
		{"it", "no.such.key", ""},
	} {
		if got := m.Text(c.lang, c.key); got != c.want {
			t.Errorf("Text(%q, %q) = %q, want %q", c.lang, c.key, got, c.want)
		}
	}
	missing := m.MissingTexts("it")
	if len(missing) == 0 || missing[0] > missing[len(missing)-1] {
		t.Fatalf("MissingTexts(it) = %v", missing)
	}
	for _, k := range missing {
		if k == "app.description" || k == "location.models.label" {
			t.Errorf("%s is translated but reported missing", k)
		}
	}
}

func TestComponentsForASystem(t *testing.T) {
	doc := mutate(t, func(d map[string]any) {
		mk := func(id string, plat ...any) map[string]any {
			c := map[string]any{"id": id, "label": id, "url": "https://example.invalid/" + id, "sha256": strings.Repeat("a", 64), "size": 10, "location": "install", "kind": "zip"}
			if plat != nil {
				c["platforms"] = plat
			}
			return c
		}
		d["components"] = []any{mk("all"), mk("win", "windows"), mk("winarm", "windows/arm64"), mk("linux", "linux", "darwin/arm64")}
	})
	m, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	ids := func(goos, goarch string) string {
		var out []string
		for _, c := range m.ComponentsFor(goos, goarch) {
			out = append(out, c.ID)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct{ goos, goarch, want string }{
		{"windows", "amd64", "all,win"},
		{"windows", "arm64", "all,win,winarm"},
		{"linux", "amd64", "all,linux"},
		{"darwin", "arm64", "all,linux"},
		{"darwin", "amd64", "all"},
	} {
		if got := ids(c.goos, c.goarch); got != c.want {
			t.Errorf("%s/%s: %s, want %s", c.goos, c.goarch, got, c.want)
		}
	}
	if got := m.NeedBytesFor("install", "windows", "amd64"); got != 20 {
		t.Errorf("NeedBytesFor windows/amd64 = %d, want 20", got)
	}
}

// The projects' own apps are bilingual: every example has an Italian text for
// everything that has an English one.
func TestExamplesAreFullyTranslatedToItalian(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.json")
	for _, f := range files {
		m, err := Load(f)
		if err != nil {
			t.Fatal(err)
		}
		if missing := m.MissingTexts("it"); len(missing) != 0 {
			t.Errorf("%s has no Italian text for %v", filepath.Base(f), missing)
		}
	}
}

func TestLaunchIsRequiredForShortcuts(t *testing.T) {
	withShortcut := func(d map[string]any) {
		d["shortcuts"] = map[string]any{"desktop": map[string]any{"offer": true}}
	}
	// Offering a shortcut with nothing to start is an error.
	if _, err := Parse(strings.NewReader(mutate(t, withShortcut))); err == nil || !strings.Contains(err.Error(), "launch is required") {
		t.Fatalf("got %v, want launch is required", err)
	}
	// With a launch, it is valid and found per system.
	doc := mutate(t, func(d map[string]any) {
		withShortcut(d)
		d["launch"] = map[string]any{"location": "install", "path": map[string]any{"windows": "bin/App.exe"}}
	})
	m, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	loc, rel, ok := m.LaunchPath("windows")
	if !ok || loc != "install" || rel != filepath.Join("bin", "App.exe") {
		t.Fatalf("LaunchPath(windows) = %q %q %v", loc, rel, ok)
	}
	if _, _, ok := m.LaunchPath("linux"); ok {
		t.Fatal("a system with no program must say so")
	}
}

func TestLaunchIsChecked(t *testing.T) {
	for name, launch := range map[string]map[string]any{
		"unknown location": {"location": "nowhere", "path": map[string]any{"windows": "a.exe"}},
		"no path":          {"location": "install"},
		"unknown system":   {"location": "install", "path": map[string]any{"beos": "a"}},
		"escapes":          {"location": "install", "path": map[string]any{"windows": "../a.exe"}},
		"absolute":         {"location": "install", "path": map[string]any{"linux": "/bin/sh"}},
		"backslash":        {"location": "install", "path": map[string]any{"windows": `bin\\a.exe`}},
	} {
		doc := mutate(t, func(d map[string]any) { d["launch"] = launch })
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: a launch like this must be refused", name)
		}
	}
}
