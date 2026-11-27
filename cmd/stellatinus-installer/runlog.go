package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// keepLogs is how many run logs are kept per app; older ones are deleted.
const keepLogs = 5

// runLog is the log of one run of install, uninstall or repair: one JSON line
// for each event, written next to the installation record (never inside a
// folder the user chose, so a removal does not take it). Entries are held in
// memory until commit, so a run that only shows a plan or a dry run leaves no
// file. A failure to write never changes the outcome of the run: it is
// reported once and logging stops.
type runLog struct {
	dir, command string
	buf          [][]byte
	f            *os.File
	path         string
	failed       bool
	warn         func(string)
}

func newRunLog(recordPath, command string, warn func(string)) *runLog {
	return &runLog{dir: filepath.Join(filepath.Dir(recordPath), "log"), command: command, warn: warn}
}

// add records one entry. It never fails.
func (l *runLog) add(kind string, fields map[string]any) {
	if l == nil || l.failed {
		return
	}
	e := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "event": kind}
	for k, v := range fields {
		if k != "time" && k != "event" {
			e[k] = v
		}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	if l.f == nil {
		l.buf = append(l.buf, b)
		return
	}
	if _, err := l.f.Write(b); err != nil {
		l.giveUp(err)
	}
}

// commit creates the file, writes what was held and from now on writes each
// entry as it comes, so a run that is killed still leaves its log.
func (l *runLog) commit() {
	if l == nil || l.f != nil || l.failed {
		return
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		l.giveUp(err)
		return
	}
	name := time.Now().UTC().Format("20060102T150405.000000Z") + "-" + l.command + ".log"
	f, err := os.OpenFile(filepath.Join(l.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		l.giveUp(err)
		return
	}
	l.f, l.path = f, f.Name()
	for _, b := range l.buf {
		if _, err := f.Write(b); err != nil {
			l.giveUp(err)
			return
		}
	}
	l.buf = nil
	l.rotate()
}

// rotate deletes the oldest logs beyond keepLogs.
func (l *runLog) rotate() {
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // names start with a UTC timestamp
	for len(names) > keepLogs {
		_ = os.Remove(filepath.Join(l.dir, names[0]))
		names = names[1:]
	}
}

func (l *runLog) giveUp(err error) {
	l.failed = true
	l.buf = nil
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
	if l.warn != nil {
		l.warn(fmt.Sprintf("could not write the log in %s: %v (the run goes on)", l.dir, err))
	}
}

// close closes the file.
func (l *runLog) close() {
	if l != nil && l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// Path is the log file, or "" while nothing is written.
func (l *runLog) Path() string {
	if l == nil || l.f == nil {
		return ""
	}
	return l.path
}
