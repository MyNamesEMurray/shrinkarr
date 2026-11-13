package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Libraries   []string
	StatePath   string
	TempDir     string
	Listen      string
	Interval    time.Duration
	MinAge      time.Duration
	VMAFFloor   float64
	VMAFThreads int
	CQMin       int
	CQMax       int
	DryRun      bool
	Arrs        []Arr
	PathFrom    string
	PathTo      string
}

var cfg Config

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	n, err := strconv.Atoi(env(k, ""))
	if err != nil {
		return def
	}
	return n
}

func loadConfig() {
	floor, err := strconv.ParseFloat(env("VMAF_FLOOR", "95"), 64)
	if err != nil {
		log.Fatal("bad VMAF_FLOOR")
	}
	cfg = Config{
		Libraries:   strings.Split(env("LIBRARIES", "/media"), ","),
		StatePath:   env("STATE", "/config/state.json"),
		TempDir:     env("TEMP_DIR", "/tmp"),
		Listen:      env("LISTEN", ":8265"),
		Interval:    time.Duration(envInt("SCAN_MINUTES", 30)) * time.Minute,
		MinAge:      time.Duration(envInt("MIN_AGE_MINUTES", 10)) * time.Minute,
		VMAFFloor:   floor,
		VMAFThreads: envInt("VMAF_THREADS", 2),
		CQMin:       envInt("CQ_MIN", 18),
		CQMax:       envInt("CQ_MAX", 32),
		DryRun:      env("DRY_RUN", "") == "1",
	}
	// ARRS=radarr|http://radarr:7878|key,sonarr|http://sonarr:8989|key
	for _, s := range strings.Split(env("ARRS", ""), ",") {
		if f := strings.Split(s, "|"); len(f) == 3 {
			cfg.Arrs = append(cfg.Arrs, Arr{Kind: f[0], URL: f[1], Key: f[2]})
		}
	}
	if from, to, ok := strings.Cut(env("PATH_MAP", ""), ":"); ok {
		cfg.PathFrom, cfg.PathTo = from, to
	}
}

type Entry struct {
	Path    string
	Size    int64
	Mtime   int64
	Status  string // queued, running, done, skipped, failed
	Detail  string
	Updated time.Time
}

var (
	mu    sync.Mutex
	state = map[string]*Entry{}
	logs  []string
)

func logf(format string, a ...any) {
	log.Printf(format, a...)
	mu.Lock()
	logs = append(logs, time.Now().Format("01-02 15:04:05 ")+fmt.Sprintf(format, a...))
	if len(logs) > 200 {
		logs = logs[len(logs)-200:]
	}
	mu.Unlock()
}

func loadState() {
	b, err := os.ReadFile(cfg.StatePath)
	if err == nil {
		if err := json.Unmarshal(b, &state); err != nil {
			log.Fatalf("corrupt state %s: %v", cfg.StatePath, err)
		}
	}
}

// saveState must be called with mu held.
func saveState() {
	b, _ := json.Marshal(state)
	tmp := cfg.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		os.Rename(tmp, cfg.StatePath)
	}
}

func set(path, status, detail string) {
	mu.Lock()
	defer mu.Unlock()
	e := state[path]
	if e == nil {
		e = &Entry{Path: path}
		state[path] = e
	}
	if fi, err := os.Stat(path); err == nil {
		e.Size, e.Mtime = fi.Size(), fi.ModTime().Unix()
	}
	e.Status, e.Detail, e.Updated = status, detail, time.Now()
	saveState()
}

var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true, ".m2ts": true, ".mov": true, ".wmv": true}

// scan returns files that are new or changed since last seen.
func scan() []string {
	var todo []string
	for _, lib := range cfg.Libraries {
		filepath.WalkDir(lib, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") || !videoExt[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			fi, err := d.Info()
			if err != nil || time.Since(fi.ModTime()) < cfg.MinAge {
				return nil // still downloading or being moved
			}
			mu.Lock()
			e := state[p]
			seen := e != nil && e.Size == fi.Size() && e.Mtime == fi.ModTime().Unix()
			mu.Unlock()
			if !seen {
				todo = append(todo, p)
			}
			return nil
		})
	}
	return todo
}

func worker() {
	for {
		todo := scan()
		for _, p := range todo {
			set(p, "queued", "")
		}
		for _, p := range todo {
			set(p, "running", "")
			logf("start %s", p)
			status, detail, final := process(p)
			logf("%s %s: %s", status, p, detail)
			if final != "" && final != p {
				mu.Lock()
				delete(state, p)
				mu.Unlock()
			}
			if final != "" {
				set(final, status, detail)
				notifyArrs(final)
			} else {
				set(p, status, detail)
			}
		}
		time.Sleep(cfg.Interval)
	}
}

var page = template.Must(template.New("p").Parse(`<!doctype html><meta name=viewport content="width=device-width">
<meta http-equiv=refresh content=15><title>tinydarr</title>
<style>body{font:14px system-ui;margin:1em}table{border-collapse:collapse;width:100%}td,th{padding:4px;border-bottom:1px solid #ddd;text-align:left}
.failed{color:#b00}.running{color:#06c;font-weight:bold}.done{color:#080}pre{background:#f4f4f4;padding:8px;overflow:auto;max-height:300px}</style>
<h1>tinydarr</h1><p>{{range $k, $v := .Counts}}{{$k}}: {{$v}} &nbsp; {{end}}</p>
<table><tr><th>Status</th><th>File</th><th>Detail</th><th>Updated</th></tr>
{{range .Rows}}<tr class="{{.Status}}"><td>{{.Status}}</td><td>{{.Path}}</td><td>{{.Detail}}</td><td>{{.Updated.Format "01-02 15:04"}}</td></tr>{{end}}
</table><h2>Log</h2><pre>{{range .Logs}}{{.}}
{{end}}</pre>`))

func ui(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	counts := map[string]int{}
	var rows []Entry
	for _, e := range state {
		counts[e.Status]++
		if e.Status != "skipped" || r.URL.Query().Has("all") {
			rows = append(rows, *e)
		}
	}
	lg := append([]string{}, logs...)
	mu.Unlock()
	rank := map[string]int{"running": 0, "queued": 1, "failed": 2}
	sort.Slice(rows, func(i, j int) bool {
		ri, ok := rank[rows[i].Status]
		if !ok {
			ri = 3
		}
		rj, ok := rank[rows[j].Status]
		if !ok {
			rj = 3
		}
		if ri != rj {
			return ri < rj
		}
		return rows[i].Updated.After(rows[j].Updated)
	})
	if len(rows) > 500 {
		rows = rows[:500]
	}
	page.Execute(w, map[string]any{"Counts": counts, "Rows": rows, "Logs": lg})
}

func main() {
	loadConfig()
	loadState()
	// Jobs interrupted by a restart go back to the queue.
	for _, e := range state {
		if e.Status == "running" || e.Status == "queued" {
			e.Mtime = 0
		}
	}
	go worker()
	http.HandleFunc("/", ui)
	logf("listening on %s, libraries %v, dry-run %v", cfg.Listen, cfg.Libraries, cfg.DryRun)
	log.Fatal(http.ListenAndServe(cfg.Listen, nil))
}
