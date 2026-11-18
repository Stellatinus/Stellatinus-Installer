package record

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestSaveLoadRoundTripLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	r := New("haedus", "1.4.0", "user", map[string]string{"install": filepath.Join(dir, "app")})
	if err := r.Add(Entry{Path: filepath.Join(dir, "app", "core.dll"), Kind: KindFile, Owner: OwnerInstaller, Size: 10, Location: "install", Origin: "core"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state", "record.json")
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AppID != "haedus" || len(got.Entries) != 1 || got.Entries[0].Origin != "core" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "state", ".record-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestAddValidates(t *testing.T) {
	r := New("x", "1.0.0", "user", map[string]string{"install": t.TempDir()})
	for name, e := range map[string]Entry{
		"relative":         {Path: "a/b", Kind: KindFile, Owner: OwnerInstaller, Location: "install"},
		"bad kind":         {Path: filepath.Join(t.TempDir(), "a"), Kind: "socket", Owner: OwnerInstaller, Location: "install"},
		"bad owner":        {Path: filepath.Join(t.TempDir(), "a"), Kind: KindFile, Owner: "root", Location: "install"},
		"unknown location": {Path: filepath.Join(t.TempDir(), "a"), Kind: KindFile, Owner: OwnerInstaller, Location: "nope"},
	} {
		if err := r.Add(e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadRejectsWrongVersionAndBadEntries(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	write(t, p, `{"version": 99}`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("wrong version accepted: %v", err)
	}
	write(t, p, `{"version": 1, "locations": {}, "entries": [{"path":"rel","kind":"file","owner":"installer","location":"x"}]}`)
	if _, err := Load(p); err == nil {
		t.Fatal("a relative path in the file was accepted")
	}
}

// The AI models folder holds models the installer downloaded next to models
// that were already there. Only the first kind may ever be removed.
func TestMixedModelsFolder(t *testing.T) {
	root := t.TempDir()
	models := filepath.Join(root, "models")
	mine1, mine2 := filepath.Join(models, "ggml-large.bin"), filepath.Join(models, "ggml-small.bin")
	theirs := filepath.Join(models, "whisper-large-v3")
	write(t, mine1, "1")
	write(t, mine2, "2")
	write(t, theirs, "keep me")

	r := New("haedus", "1.4.0", "user", map[string]string{"models": models})
	_ = r.Add(Entry{Path: models, Kind: KindDir, Owner: OwnerAdopted, Location: "models"})
	_ = r.Add(Entry{Path: mine1, Kind: KindFile, Owner: OwnerInstaller, Size: 1100, Location: "models"})
	_ = r.Add(Entry{Path: mine2, Kind: KindFile, Owner: OwnerInstaller, Size: 500, Location: "models"})
	_ = r.Add(Entry{Path: theirs, Kind: KindFile, Owner: OwnerAdopted, Size: 3100, Location: "models"})

	// Keep the small one; remove the large one. Even asking to remove
	// everything in the location (no keep choices) the adopted ones stay.
	items := r.Plan(Choices{KeepPath: map[string]bool{mine2: true}})
	actions := map[string]Action{}
	for _, it := range items {
		actions[filepath.Base(it.Path)] = it.Action
	}
	if actions["ggml-large.bin"] != ActionRemove || actions["ggml-small.bin"] != ActionKeep ||
		actions["whisper-large-v3"] != ActionProtected || actions["models"] != ActionProtected {
		t.Fatalf("unexpected plan: %v", actions)
	}
	rm, keep, prot := Totals(items)
	if rm != 1100 || keep != 500 || prot != 3100 {
		t.Fatalf("totals remove %d keep %d protected %d", rm, keep, prot)
	}

	rep := r.Apply(items)
	if len(rep.Failed) != 0 {
		t.Fatalf("failures: %+v", rep.Failed)
	}
	if exists(mine1) || !exists(mine2) || !exists(theirs) || !exists(models) {
		t.Fatal("removal touched the wrong files")
	}
}

func TestEverythingTheInstallerCreatedIsRemovedAndEmptyFoldersToo(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	f1, f2 := filepath.Join(app, "bin", "a.dll"), filepath.Join(app, "b.txt")
	write(t, f1, "a")
	write(t, f2, "b")
	r := New("x", "1.0.0", "user", map[string]string{"install": app})
	_ = r.Add(Entry{Path: app, Kind: KindDir, Owner: OwnerInstaller, Location: "install"})
	_ = r.Add(Entry{Path: filepath.Join(app, "bin"), Kind: KindDir, Owner: OwnerInstaller, Location: "install"})
	_ = r.Add(Entry{Path: f1, Kind: KindFile, Owner: OwnerInstaller, Location: "install"})
	_ = r.Add(Entry{Path: f2, Kind: KindFile, Owner: OwnerInstaller, Location: "install"})
	rep := r.Apply(r.Plan(Choices{}))
	if len(rep.Failed) != 0 || exists(app) {
		t.Fatalf("not fully removed: %+v", rep)
	}
	// A second run is harmless.
	if rep := r.Apply(r.Plan(Choices{})); len(rep.Failed) != 0 {
		t.Fatalf("second run failed: %+v", rep.Failed)
	}
}

func TestAFolderWithSomethingNotOursIsLeft(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	write(t, filepath.Join(app, "ours.txt"), "o")
	write(t, filepath.Join(app, "users-notes.txt"), "u") // not recorded
	r := New("x", "1.0.0", "user", map[string]string{"install": app})
	_ = r.Add(Entry{Path: app, Kind: KindDir, Owner: OwnerInstaller, Location: "install"})
	_ = r.Add(Entry{Path: filepath.Join(app, "ours.txt"), Kind: KindFile, Owner: OwnerInstaller, Location: "install"})
	rep := r.Apply(r.Plan(Choices{}))
	if !exists(filepath.Join(app, "users-notes.txt")) || exists(filepath.Join(app, "ours.txt")) {
		t.Fatal("wrong files removed")
	}
	if len(rep.Failed) != 1 || !strings.Contains(rep.Failed[0].Err.Error(), "not empty") {
		t.Fatalf("the non-empty folder should be reported, got %+v", rep.Failed)
	}
}

func TestNeverFollowsASymbolicLinkOutOfTheInstall(t *testing.T) {
	root := t.TempDir()
	app, outside := filepath.Join(root, "app"), filepath.Join(root, "outside")
	secret := filepath.Join(outside, "secret.txt")
	write(t, secret, "do not delete")
	write(t, filepath.Join(app, "x"), "x")
	link := filepath.Join(app, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
	r := New("x", "1.0.0", "user", map[string]string{"install": app})
	// A record entry that reaches the outside file through the link.
	_ = r.Add(Entry{Path: filepath.Join(link, "secret.txt"), Kind: KindFile, Owner: OwnerInstaller, Location: "install"})
	rep := r.Apply(r.Plan(Choices{}))
	if !exists(secret) {
		t.Fatal("a file outside the install was deleted through a symbolic link")
	}
	if len(rep.Failed) != 1 || !strings.Contains(rep.Failed[0].Err.Error(), "symbolic link") {
		t.Fatalf("expected a refusal, got %+v", rep.Failed)
	}

	// The link itself, recorded as a file, is removed without touching its target.
	r2 := New("x", "1.0.0", "user", map[string]string{"install": app})
	_ = r2.Add(Entry{Path: link, Kind: KindFile, Owner: OwnerInstaller, Location: "install"})
	if rep := r2.Apply(r2.Plan(Choices{})); len(rep.Failed) != 0 {
		t.Fatalf("%+v", rep.Failed)
	}
	if exists(link) || !exists(secret) {
		t.Fatal("removing the link must not touch its target")
	}
}

func TestRefusesAPathOutsideItsLocation(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	victim := filepath.Join(root, "victim.txt")
	write(t, victim, "v")
	write(t, filepath.Join(app, "x"), "x")
	r := New("x", "1.0.0", "user", map[string]string{"install": app})
	r.Entries = append(r.Entries, Entry{Path: victim, Kind: KindFile, Owner: OwnerInstaller, Location: "install"}) // as if a hand-edited file
	rep := r.Apply(r.Plan(Choices{}))
	if !exists(victim) || len(rep.Failed) != 1 {
		t.Fatalf("a path outside its location must be refused: %+v", rep)
	}
}

func TestKeepLocationKeepsWhatTheInstallerMadeThere(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "out")
	f := filepath.Join(out, "t1.txt")
	write(t, f, "t")
	r := New("x", "1.0.0", "user", map[string]string{"output": out})
	_ = r.Add(Entry{Path: f, Kind: KindFile, Owner: OwnerInstaller, Location: "output"})
	rep := r.Apply(r.Plan(Choices{KeepLocation: map[string]bool{"output": true}}))
	if !exists(f) || len(rep.Kept) != 1 {
		t.Fatalf("kept location was touched: %+v", rep)
	}
}

func TestOwnerAndRemove(t *testing.T) {
	root := t.TempDir()
	r := New("x", "1.0.0", "user", map[string]string{"install": root})
	f := filepath.Join(root, "a.txt")
	_ = r.Add(Entry{Path: f, Kind: KindFile, Owner: OwnerAdopted, Location: "install"})
	if o, ok := r.Owner(f); !ok || o != OwnerAdopted {
		t.Fatalf("Owner = %q %v", o, ok)
	}
	if _, ok := r.Owner(filepath.Join(root, "missing")); ok {
		t.Fatal("an unrecorded path has no owner")
	}
	if !r.Remove(f) || r.Remove(f) {
		t.Fatal("Remove should report true once, then false")
	}
	if len(r.Entries) != 0 {
		t.Fatalf("entries left: %v", r.Entries)
	}
}
