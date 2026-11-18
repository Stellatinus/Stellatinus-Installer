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
	if got := m.NeedBytes("install"); got != 1000 {
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
		"dest with dot dot":  {func(d map[string]any) { d["components"].([]any)[0].(map[string]any)["dest"] = "a/../b" }, "components[0].dest"},
		"absolute dest":      {func(d map[string]any) { d["components"].([]any)[0].(map[string]any)["dest"] = "/etc" }, "components[0].dest"},
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
		"shortcuts": reflect.TypeOf(Shortcuts{}), "shortcut": reflect.TypeOf(Shortcut{}),
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
