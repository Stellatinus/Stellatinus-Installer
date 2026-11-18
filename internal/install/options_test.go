package install

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
)

func withPortConfig(doc map[string]any) {
	doc["options"] = []any{map[string]any{"id": "port", "label": "Port", "kind": "port", "default": 5080}}
	doc["config"] = []any{map[string]any{"location": "install", "name": "config.json", "format": "json", "values": map[string]any{"port": "port"}}}
}

func TestAConfigFileIsWrittenFromTheAnswersAndRemovedWithTheRest(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.root)
	data := bytes.Repeat([]byte("a"), 300)
	srv := tlsServer(t, serveBytes(data))
	m := buildX(t, withPortConfig, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	opt := e.opts(srv)
	opt.Answers = map[string]int{"port": 5099}
	res, err := Run(context.Background(), m, opt)
	if err != nil {
		t.Fatal(err)
	}
	b, rerr := os.ReadFile(filepath.Join(e.paths["install"], "config.json"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	var got map[string]int
	if err := json.Unmarshal(b, &got); err != nil || got["port"] != 5099 {
		t.Fatalf("config.json = %s (%v)", b, err)
	}
	if o, ok := res.Record.Owner(filepath.Join(e.paths["install"], "config.json")); !ok || o != record.OwnerInstaller {
		t.Fatalf("the config file must be recorded as the installer's: %v %v", o, ok)
	}
	for _, s := range snapshot(t, e.root) {
		if strings.Contains(s, ".stellatinus-tmp") {
			t.Errorf("temporary file left: %s", s)
		}
	}
	if rep := res.Record.Apply(res.Record.Plan(record.Choices{})); len(rep.Failed) != 0 {
		t.Fatalf("removal: %+v", rep.Failed)
	}
	if after := snapshot(t, e.root); !equal(after, append(before, "d state", "f state/record.json")) && !equal(after, append(before, "d state", "f state"+string(filepath.Separator)+"record.json")) {
		t.Fatalf("not clean after removal: %v", after)
	}
}

func TestTheDefaultAnswerIsUsedWhenNoneIsGiven(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("a"), 300)
	srv := tlsServer(t, serveBytes(data))
	m := buildX(t, withPortConfig, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	if _, err := Run(context.Background(), m, e.opts(srv)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(e.paths["install"], "config.json"))
	if !strings.Contains(string(b), "5080") {
		t.Fatalf("config.json = %s, want the default port", b)
	}
}

func TestBadAnswersAreRefusedBeforeAnythingIsDone(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 300)
	var hits atomic.Int32
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write(data) }))
	for name, answers := range map[string]map[string]int{
		"a privileged port": {"port": 80},
		"a port too large":  {"port": 70000},
		"an unknown option": {"nope": 1},
	} {
		e := newEnv(t)
		before := snapshot(t, e.root)
		m := buildX(t, withPortConfig, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
		opt := e.opts(srv)
		opt.Answers = answers
		if _, err := Run(context.Background(), m, opt); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if !equal(snapshot(t, e.root), before) {
			t.Errorf("%s: something was created", name)
		}
	}
	if hits.Load() != 0 {
		t.Error("a bad answer must not download anything")
	}
}

func TestAnExistingConfigFileIsNeverOverwritten(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.paths["install"], 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(e.paths["install"], "config.json")
	if err := os.WriteFile(theirs, []byte(`{"port": 1234}`), 0o644); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("a"), 300)
	srv := tlsServer(t, serveBytes(data))
	m := buildX(t, withPortConfig, comp{id: "f", location: "install", name: "f.bin", url: srv.URL + "/f", data: data})
	_, err := Run(context.Background(), m, e.opts(srv))
	if err == nil || !strings.Contains(err.Error(), "not overwriting") {
		t.Fatalf("want a refusal, got %v", err)
	}
	mustRolledBack(t, err)
	if b, _ := os.ReadFile(theirs); string(b) != `{"port": 1234}` {
		t.Fatal("the user's config file was changed")
	}
	if _, err := os.Stat(filepath.Join(e.paths["install"], "f.bin")); err == nil {
		t.Fatal("the downloaded file was not rolled back")
	}
}

func TestOnlyTheComponentsForThisSystemAreInstalled(t *testing.T) {
	e := newEnv(t)
	win := bytes.Repeat([]byte("w"), 200)
	lin := bytes.Repeat([]byte("l"), 200)
	mac := bytes.Repeat([]byte("m"), 200)
	var asked []string
	mux := http.NewServeMux()
	for path, d := range map[string][]byte{"/win": win, "/lin": lin, "/mac": mac} {
		path, d := path, d
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { asked = append(asked, path); _, _ = w.Write(d) })
	}
	srv := tlsServer(t, mux)
	m := build(t,
		comp{id: "win", location: "install", name: "win.bin", url: srv.URL + "/win", data: win, platforms: []string{"windows/amd64"}},
		comp{id: "lin", location: "install", name: "lin.bin", url: srv.URL + "/lin", data: lin, platforms: []string{"linux"}},
		comp{id: "mac", location: "install", name: "mac.bin", url: srv.URL + "/mac", data: mac, platforms: []string{"darwin/arm64"}},
	)
	var events []Event
	opt := e.opts(srv)
	opt.GOOS, opt.GOARCH = "linux", "arm64"
	opt.Events = func(ev Event) { events = append(events, ev) }
	if _, err := Run(context.Background(), m, opt); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "/lin" {
		t.Fatalf("requested %v, want only /lin", asked)
	}
	if _, err := os.Stat(filepath.Join(e.paths["install"], "win.bin")); err == nil {
		t.Fatal("the Windows component was installed on Linux")
	}
	for _, ev := range events {
		if ev.Component != "" && ev.Count != 1 {
			t.Fatalf("the progress counts %d components, want 1: %+v", ev.Count, ev)
		}
	}
}
