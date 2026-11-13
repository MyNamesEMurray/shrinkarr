package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

var videoExt = []string{".mkv", ".mp4", ".m4v", ".avi", ".ts", ".m2ts", ".mov", ".wmv", ".mpg", ".mpeg", ".webm", ".flv"}

var (
	qMu      sync.Mutex
	queue    []string // paths, in order
	running  = map[string]*Job{}
	lastScan = map[string]time.Time{}
	scanning = map[string]bool{}
	scanNow  = make(chan string, 8)
)

func enqueue(path, lib string, front bool) {
	qMu.Lock()
	defer qMu.Unlock()
	if slices.Contains(queue, path) {
		return
	}
	for _, j := range running {
		if j.Path == path {
			return
		}
	}
	if front {
		queue = append([]string{path}, queue...)
	} else {
		queue = append(queue, path)
	}
	setFile(path, lib, "queued", "")
}

func unqueue(path string) {
	qMu.Lock()
	queue = slices.DeleteFunc(queue, func(p string) bool { return p == path })
	qMu.Unlock()
}

func inSchedule(s Settings, now time.Time) bool {
	if s.ActiveFrom == "" || s.ActiveTo == "" || s.ActiveFrom == s.ActiveTo {
		return true
	}
	t := now.Format("15:04")
	if s.ActiveFrom < s.ActiveTo {
		return t >= s.ActiveFrom && t < s.ActiveTo
	}
	return t >= s.ActiveFrom || t < s.ActiveTo // window crosses midnight
}

func libraryFor(path string) (Library, bool) {
	for _, l := range cfg().Libraries {
		if strings.HasPrefix(path, strings.TrimRight(l.Path, "/")+"/") {
			return l, true
		}
	}
	return Library{}, false
}

func scanLibrary(l Library) int {
	qMu.Lock()
	if scanning[l.ID] {
		qMu.Unlock()
		return 0
	}
	scanning[l.ID] = true
	qMu.Unlock()
	defer func() {
		qMu.Lock()
		scanning[l.ID] = false
		lastScan[l.ID] = time.Now()
		qMu.Unlock()
	}()
	minAge := time.Duration(cfg().MinAgeMinutes) * time.Minute
	testMode := cfg().TestMode
	added := 0
	filepath.WalkDir(l.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && p != l.Path {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if strings.HasPrefix(name, ".shrinkarr-") {
			if time.Since(fi.ModTime()) > 24*time.Hour {
				os.Remove(p) // temp file left by a crash
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !slices.Contains(videoExt, strings.ToLower(filepath.Ext(name))) {
			return nil
		}
		if time.Since(fi.ModTime()) < minAge {
			return nil // still downloading or being copied
		}
		storeMu.Lock()
		f := files[p]
		known := f != nil && f.Size == fi.Size() && f.Mtime == fi.ModTime().Unix() && f.Status != "queued" && !(f.Test && !testMode)
		ignored := f != nil && f.Status == "ignored"
		storeMu.Unlock()
		if !known && !ignored {
			enqueue(p, l.ID, false)
			added++
		}
		return nil
	})
	log.Printf("scan %s: %d queued", l.Name, added)
	return added
}

func scanner() {
	tick := time.NewTicker(time.Minute)
	for {
		select {
		case id := <-scanNow:
			for _, l := range cfg().Libraries {
				if id == "" || l.ID == id {
					go scanLibrary(l)
				}
			}
		case <-tick.C:
			for _, l := range cfg().Libraries {
				qMu.Lock()
				due := l.Enabled && time.Since(lastScan[l.ID]) > time.Duration(l.ScanMinutes)*time.Minute
				qMu.Unlock()
				if due {
					go scanLibrary(l)
				}
			}
		}
	}
}

// dispatcher starts jobs while there are free worker slots. Re-reads settings each tick.
func dispatcher() {
	for range time.Tick(2 * time.Second) {
		s := cfg()
		if s.Paused || !inSchedule(s, time.Now()) {
			continue
		}
		qMu.Lock()
		if len(running) >= s.Workers || len(queue) == 0 {
			qMu.Unlock()
			continue
		}
		path := queue[0]
		queue = queue[1:]
		qMu.Unlock()

		lib, ok := libraryFor(path)
		if _, err := os.Stat(path); err != nil || !ok || !lib.Enabled {
			dropFile(path)
			continue
		}
		flow := getFlow(lib.FlowID)
		if flow == nil {
			setFile(path, lib.ID, "failed", "library has no flow; pick one on the Libraries page")
			continue
		}
		j := newJob(path, lib, flow, s.TestMode)
		qMu.Lock()
		running[j.ID] = j
		qMu.Unlock()
		setFile(path, lib.ID, "running", "")
		go func() {
			r := j.execute()
			qMu.Lock()
			delete(running, j.ID)
			qMu.Unlock()
			finish(j, r)
		}()
	}
}

func newJob(path string, lib Library, flow *Flow, test bool) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	return &Job{ID: randID(), Path: path, Cur: path, Lib: lib, Flow: flow, ctx: ctx, cancel: cancel, TestMode: test}
}

// execute runs the flow and always cleans up temp files. Never deletes the original on error.
func (j *Job) execute() Record {
	r := Record{ID: j.ID, Path: j.Path, Library: j.Lib.ID, Flow: j.Flow.Name, Started: time.Now()}
	defer j.cancel()
	err := func() error {
		fi, err := os.Stat(j.Path)
		if err != nil {
			return err
		}
		j.OrigSize = fi.Size()
		if j.Orig, err = ffprobe(j.ctx, j.Path); err != nil {
			return err
		}
		j.Probe = j.Orig
		return j.runFlow()
	}()
	for _, t := range j.temps {
		os.Remove(t)
	}
	switch {
	case err != nil:
		j.Status, j.Detail = "failed", err.Error()
	case j.Status != "":
	case j.Replaced:
		j.Status, j.Detail = "done", strings.Join(j.notes, ", ")
	case len(j.temps) > 0:
		j.Status, j.Detail = "skipped", "flow ended without a Replace original block; nothing changed"
	default:
		j.Status, j.Detail = "skipped", "nothing to do"
	}
	r.Status, r.Detail, r.Finished = j.Status, j.Detail, time.Now()
	r.OrigSize, r.NewSize, r.Final = j.OrigSize, j.OrigSize, j.Final
	if v := j.Orig.Video(); v != nil {
		r.OrigCodec, r.NewCodec = v.CodecName, v.CodecName
	}
	if j.Replaced {
		if fi, err := os.Stat(j.Final); err == nil {
			r.NewSize = fi.Size()
		}
		if v := j.Probe.Video(); v != nil {
			r.NewCodec = v.CodecName
		}
	}
	r.Steps, r.Nodes, r.Commands = j.Steps, j.Visited, j.Commands
	j.progMu.Lock()
	r.Log = tail(j.log.String(), 20000)
	j.progMu.Unlock()
	return r
}

func finish(j *Job, r Record) {
	addRecord(r)
	detail := r.Detail
	if r.Status == "done" {
		detail = fmt.Sprintf("%s → %s. %s", human(r.OrigSize), human(r.NewSize), r.Detail)
	}
	if j.Replaced && j.Final != j.Path {
		dropFile(j.Path)
	}
	path := j.Path
	if j.Replaced {
		path = j.Final
	}
	setFile(path, j.Lib.ID, r.Status, detail)
	storeMu.Lock()
	if f := files[path]; f != nil {
		f.JobID, f.Test = r.ID, j.TestMode
	}
	storeMu.Unlock()
	if r.Status == "failed" {
		discord(fmt.Sprintf("Shrinkarr failed on %s: %s", filepath.Base(j.Path), tail(r.Detail, 300)))
	}
	log.Printf("%s %s: %s", r.Status, j.Path, r.Detail)
}

func cancelJob(id string) bool {
	qMu.Lock()
	defer qMu.Unlock()
	if j := running[id]; j != nil {
		j.cancel()
		return true
	}
	return false
}

type RunningInfo struct {
	ID       string   `json:"id"`
	Path     string   `json:"path"`
	Flow     string   `json:"flow"`
	Progress Progress `json:"progress"`
}

func runningJobs() []RunningInfo {
	qMu.Lock()
	defer qMu.Unlock()
	var out []RunningInfo
	for _, j := range running {
		out = append(out, RunningInfo{ID: j.ID, Path: j.Path, Flow: j.Flow.Name, Progress: j.Progress()})
	}
	slices.SortFunc(out, func(a, b RunningInfo) int { return strings.Compare(a.Path, b.Path) })
	return out
}
