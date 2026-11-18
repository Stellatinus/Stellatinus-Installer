package install

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
	"github.com/Stellatinus/Stellatinus-Installer/internal/record"
	"github.com/Stellatinus/Stellatinus-Installer/internal/safepath"
)

// A file that unpacks to more than ratioMinSize bytes with a ratio above
// ratioLimit is treated as a zip bomb.
const (
	ratioMinSize = 4 << 20
	ratioLimit   = 500
)

type zipTarget struct {
	f      *zip.File
	rel    string
	target string
	isDir  bool
}

// installZip downloads a zip archive and unpacks it under base. Before it
// writes a single file it checks every entry: the path must stay inside base
// and be valid on every system, symbolic links and special files are refused,
// nothing may already exist at a target (an existing file is never
// overwritten), no two entries may differ only in case, and the archive must
// not be too large, too numerous or suspiciously compressed.
func (r *runner) installZip(ctx context.Context, idx int, c manifest.Component, base string) error {
	part := filepath.Join(base, ".stellatinus-"+c.ID+".zip.part")
	if err := r.track(record.Entry{Path: part, Kind: record.KindFile, Owner: record.OwnerInstaller, Location: c.Location, Origin: c.ID}); err != nil {
		return err
	}
	if err := r.save(); err != nil {
		return err
	}
	if err := r.fetch(ctx, idx, c, part); err != nil {
		return err
	}
	zr, err := zip.OpenReader(part)
	if err != nil {
		return fmt.Errorf("%s is not a readable zip archive: %w", c.Label, err)
	}
	defer func() { _ = zr.Close() }()

	targets, total, err := r.planZip(zr, base)
	if err != nil {
		return fmt.Errorf("%s: %w", c.Label, err)
	}

	var done int64
	last := time.Now()
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if t.isDir {
			if _, err := r.makeDirs(c.Location, t.target, false); err != nil {
				return err
			}
			continue
		}
		if _, err := r.makeDirs(c.Location, filepath.Dir(t.target), false); err != nil {
			return err
		}
		n, err := r.writeEntry(c, t)
		if err != nil {
			return err
		}
		done += n
		if time.Since(last) >= 100*time.Millisecond {
			r.event(idx, c, PhaseUnpack, t.rel, done, total)
			last = time.Now()
		}
	}
	r.event(idx, c, PhaseUnpack, "", total, total)

	_ = zr.Close()
	if err := os.Remove(part); err != nil {
		return err
	}
	r.untrack(part)
	r.logf("unpacked %d file(s) from %s into %s", len(targets), c.Label, base)
	return nil
}

func (r *runner) planZip(zr *zip.ReadCloser, base string) ([]zipTarget, int64, error) {
	if len(zr.File) > r.opt.MaxEntries {
		return nil, 0, fmt.Errorf("the archive has %d entries, more than the %d allowed", len(zr.File), r.opt.MaxEntries)
	}
	var (
		targets []zipTarget
		total   int64
		seen    = map[string]string{}
	)
	for _, f := range zr.File {
		if strings.Contains(f.Name, `\`) {
			return nil, 0, fmt.Errorf("the entry %q contains a backslash", f.Name)
		}
		rel, err := safepath.CleanRel(f.Name)
		if err != nil {
			return nil, 0, fmt.Errorf("the entry %q is not safe: %w", f.Name, err)
		}
		mode := f.Mode()
		isDir := f.FileInfo().IsDir()
		if mode&fs.ModeSymlink != 0 {
			return nil, 0, fmt.Errorf("the entry %q is a symbolic link; refusing it", f.Name)
		}
		if !isDir && !mode.IsRegular() {
			return nil, 0, fmt.Errorf("the entry %q is not a regular file; refusing it", f.Name)
		}
		key := strings.ToLower(rel)
		if other, dup := seen[key]; dup {
			return nil, 0, fmt.Errorf("the entries %q and %q differ only in case", other, f.Name)
		}
		seen[key] = f.Name

		if !isDir {
			if f.UncompressedSize64 > uint64(r.opt.MaxUnpacked) {
				return nil, 0, fmt.Errorf("the entry %q unpacks to more than the %d bytes allowed", f.Name, r.opt.MaxUnpacked)
			}
			total += int64(f.UncompressedSize64)
			if total > r.opt.MaxUnpacked {
				return nil, 0, fmt.Errorf("the archive unpacks to more than the %d bytes allowed", r.opt.MaxUnpacked)
			}
			if f.UncompressedSize64 > ratioMinSize && f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > ratioLimit {
				return nil, 0, fmt.Errorf("the entry %q is compressed more than %d to 1; this looks like a zip bomb", f.Name, ratioLimit)
			}
		}
		target := filepath.Join(base, rel)
		if !isDir {
			if _, err := os.Lstat(target); err == nil {
				return nil, 0, fmt.Errorf("%s already exists; not overwriting it", target)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, 0, err
			}
		}
		if isBelow(base, target) {
			if err := noSymlinks(base, filepath.Dir(target)); err != nil {
				return nil, 0, err
			}
		}
		targets = append(targets, zipTarget{f: f, rel: rel, target: target, isDir: isDir})
	}
	return targets, total, nil
}

// writeEntry unpacks one file through a temporary name, so a half-written
// file never sits at its final path, and records it.
func (r *runner) writeEntry(c manifest.Component, t zipTarget) (int64, error) {
	rc, err := t.f.Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = rc.Close() }()

	perm := fs.FileMode(0o644)
	if runtime.GOOS != "windows" && t.f.Mode()&0o111 != 0 {
		perm = 0o755
	}
	tmp := t.target + ".stellatinus-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, err
	}
	if err := r.track(record.Entry{Path: tmp, Kind: record.KindFile, Owner: record.OwnerInstaller, Location: c.Location, Origin: c.ID}); err != nil {
		_ = out.Close()
		return 0, err
	}
	want := int64(t.f.UncompressedSize64)
	n, err := io.Copy(out, io.LimitReader(rc, want+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("unpacking %s: %w", t.rel, err)
	}
	if n != want {
		return 0, fmt.Errorf("unpacking %s: the entry says %d bytes but holds %d", t.rel, want, n)
	}
	if err := os.Rename(tmp, t.target); err != nil {
		return 0, err
	}
	r.untrack(tmp)
	return n, r.track(record.Entry{Path: t.target, Kind: record.KindFile, Owner: record.OwnerInstaller, Size: n, Location: c.Location, Origin: c.ID})
}
