package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

func randID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// FileState is the latest known status of one file in a library.
type FileState struct {
	Path    string    `json:"path"`
	Library string    `json:"library"`
	Size    int64     `json:"size"`
	Mtime   int64     `json:"mtime"`
	Status  string    `json:"status"` // queued running done skipped failed ignored
	Detail  string    `json:"detail"`
	JobID   string    `json:"jobId,omitempty"`
	Test    bool      `json:"test,omitempty"` // result came from test mode; redo when test mode goes off
	Updated time.Time `json:"updated"`
}

// Record is one finished job, appended to history.jsonl and used for stats.
type Record struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Final     string    `json:"final,omitempty"`
	Library   string    `json:"library"`
	Flow      string    `json:"flow"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail"`
	OrigSize  int64     `json:"origSize"`
	NewSize   int64     `json:"newSize"`
	OrigCodec string    `json:"origCodec"`
	NewCodec  string    `json:"newCodec"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	Steps     []string  `json:"steps"`
	Nodes     []string  `json:"nodes"` // ids of blocks visited, for highlighting in the editor
	Commands  []string  `json:"commands"`
	Log       string    `json:"log"`
}

var (
	storeMu  sync.Mutex
	files    = map[string]*FileState{}
	history  []Record
	dirty    bool
	histFile *os.File
)

func loadStore() error {
	b, err := os.ReadFile(filepath.Join(configDir, "files.json"))
	if err == nil {
		if err := json.Unmarshal(b, &files); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Jobs interrupted by a restart go back to the queue.
	for _, f := range files {
		if f.Status == "running" || f.Status == "queued" {
			f.Status, f.Mtime = "queued", 0
		}
	}
	hp := filepath.Join(configDir, "history.jsonl")
	if f, err := os.Open(hp); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				history = append(history, r)
			}
		}
		f.Close()
	}
	histFile, err = os.OpenFile(hp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	go func() {
		for range time.Tick(3 * time.Second) {
			flushStore()
		}
	}()
	return nil
}

// shortcut: whole-map JSON rewrite, fine to ~50k files; move to SQLite if libraries get bigger.
func flushStore() {
	storeMu.Lock()
	if !dirty {
		storeMu.Unlock()
		return
	}
	b, _ := json.Marshal(files)
	dirty = false
	storeMu.Unlock()
	p := filepath.Join(configDir, "files.json")
	if os.WriteFile(p+".tmp", b, 0o644) == nil {
		os.Rename(p+".tmp", p)
	}
}

func setFile(path, lib, status, detail string) {
	storeMu.Lock()
	defer storeMu.Unlock()
	f := files[path]
	if f == nil {
		f = &FileState{Path: path}
		files[path] = f
	}
	if lib != "" {
		f.Library = lib
	}
	if fi, err := os.Stat(path); err == nil {
		f.Size, f.Mtime = fi.Size(), fi.ModTime().Unix()
	}
	f.Status, f.Detail, f.Updated = status, detail, time.Now()
	dirty = true
}

func dropFile(path string) {
	storeMu.Lock()
	delete(files, path)
	dirty = true
	storeMu.Unlock()
}

func addRecord(r Record) {
	b, _ := json.Marshal(r)
	storeMu.Lock()
	history = append(history, r)
	histFile.Write(append(b, '\n'))
	storeMu.Unlock()
}

func fileList(status, lib string, limit int) []FileState {
	storeMu.Lock()
	var out []FileState
	for _, f := range files {
		if (status == "" || f.Status == status) && (lib == "" || f.Library == lib) {
			out = append(out, *f)
		}
	}
	storeMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func statusCounts() map[string]int {
	storeMu.Lock()
	defer storeMu.Unlock()
	c := map[string]int{}
	for _, f := range files {
		c[f.Status]++
	}
	return c
}

type Stats struct {
	Files       int              `json:"files"`
	OrigBytes   int64            `json:"origBytes"`
	NewBytes    int64            `json:"newBytes"`
	SavedBytes  int64            `json:"savedBytes"`
	Failed      int              `json:"failed"`
	EncodeHours float64          `json:"encodeHours"`
	Daily       []DayStat        `json:"daily"`
	Libraries   map[string]int64 `json:"libraries"` // saved bytes by library name
	CodecsIn    map[string]int   `json:"codecsIn"`
	CodecsOut   map[string]int   `json:"codecsOut"`
	Top         []Record         `json:"top"`
}

type DayStat struct {
	Day   string `json:"day"`
	Saved int64  `json:"saved"`
	Files int    `json:"files"`
}

func computeStats(days int) Stats {
	libName := map[string]string{}
	for _, l := range cfg().Libraries {
		libName[l.ID] = l.Name
	}
	s := Stats{Libraries: map[string]int64{}, CodecsIn: map[string]int{}, CodecsOut: map[string]int{}}
	day := map[string]*DayStat{}
	var done []Record
	storeMu.Lock()
	for _, r := range history {
		if r.Status == "failed" {
			s.Failed++
			continue
		}
		if r.Status != "done" {
			continue
		}
		saved := r.OrigSize - r.NewSize
		s.Files++
		s.OrigBytes += r.OrigSize
		s.NewBytes += r.NewSize
		s.SavedBytes += saved
		s.EncodeHours += r.Finished.Sub(r.Started).Hours()
		name := libName[r.Library]
		if name == "" {
			name = "(removed library)"
		}
		s.Libraries[name] += saved
		s.CodecsIn[r.OrigCodec]++
		s.CodecsOut[r.NewCodec]++
		k := r.Finished.Format("2006-01-02")
		if day[k] == nil {
			day[k] = &DayStat{Day: k}
		}
		day[k].Saved += saved
		day[k].Files++
		done = append(done, r)
	}
	storeMu.Unlock()
	for i := days - 1; i >= 0; i-- {
		k := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		d := DayStat{Day: k}
		if day[k] != nil {
			d = *day[k]
		}
		s.Daily = append(s.Daily, d)
	}
	sort.Slice(done, func(i, j int) bool {
		return done[i].OrigSize-done[i].NewSize > done[j].OrigSize-done[j].NewSize
	})
	if len(done) > 10 {
		done = done[:10]
	}
	for _, r := range done {
		r.Log, r.Commands, r.Steps = "", nil, nil
		s.Top = append(s.Top, r)
	}
	return s
}

func findRecord(id string) (Record, bool) {
	storeMu.Lock()
	defer storeMu.Unlock()
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].ID == id {
			return history[i], true
		}
	}
	return Record{}, false
}

func recentRecords(limit int, status string) []Record {
	storeMu.Lock()
	defer storeMu.Unlock()
	out := []Record{}
	for i := len(history) - 1; i >= 0 && len(out) < limit; i-- {
		if status == "" || history[i].Status == status {
			r := history[i]
			r.Log, r.Commands = "", nil
			out = append(out, r)
		}
	}
	return out
}
