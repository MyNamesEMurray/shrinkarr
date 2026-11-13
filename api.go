package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

var (
	sessMu   sync.Mutex
	sessions = map[string]time.Time{}
)

func hashPassword(pw, saltHex string) string {
	salt, _ := hex.DecodeString(saltHex)
	k, _ := pbkdf2.Key(sha256.New, pw, salt, 210000, 32)
	return hex.EncodeToString(k)
}

func authed(r *http.Request) bool {
	if cfg().PasswordHash == "" {
		return true
	}
	c, err := r.Cookie("shrinkarr")
	if err != nil {
		return false
	}
	sessMu.Lock()
	defer sessMu.Unlock()
	exp, ok := sessions[c.Value]
	return ok && time.Now().Before(exp)
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readBody(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(v)
}

type handler func(w http.ResponseWriter, r *http.Request) (any, error)

// jsonOnly blocks cross-site "simple" POSTs: a page on another site can't send
// application/json without a CORS preflight, and this server never approves one.
func jsonOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if mt, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(mt) != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, errors.New("send JSON with Content-Type: application/json"))
		return false
	}
	return true
}

func api(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !jsonOnly(w, r) {
			return
		}
		if !authed(r) {
			fail(w, http.StatusUnauthorized, errors.New("login required"))
			return
		}
		v, err := h(w, r)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		writeJSONResp(w, v)
	}
}

func routes() http.Handler {
	m := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	m.Handle("GET /", http.FileServerFS(sub))

	m.HandleFunc("GET /api/auth", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(w, map[string]bool{"required": cfg().PasswordHash != "", "ok": authed(r)})
	})
	m.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		if !jsonOnly(w, r) {
			return
		}
		var b struct{ Password string }
		readBody(r, &b)
		s := cfg()
		if s.PasswordHash == "" || subtle.ConstantTimeCompare([]byte(hashPassword(b.Password, s.PasswordSalt)), []byte(s.PasswordHash)) != 1 {
			time.Sleep(time.Second) // slow down guessing
			fail(w, http.StatusUnauthorized, errors.New("wrong password"))
			return
		}
		tok := make([]byte, 32)
		rand.Read(tok)
		t := hex.EncodeToString(tok)
		sessMu.Lock()
		for k, exp := range sessions {
			if time.Now().After(exp) {
				delete(sessions, k)
			}
		}
		sessions[t] = time.Now().Add(30 * 24 * time.Hour)
		sessMu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "shrinkarr", Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 86400})
		writeJSONResp(w, map[string]bool{"ok": true})
	})
	m.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if !jsonOnly(w, r) {
			return
		}
		if c, err := r.Cookie("shrinkarr"); err == nil {
			sessMu.Lock()
			delete(sessions, c.Value)
			sessMu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "shrinkarr", Path: "/", MaxAge: -1})
		writeJSONResp(w, map[string]bool{"ok": true})
	})

	m.HandleFunc("GET /api/status", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		s := cfg()
		qMu.Lock()
		ql := len(queue)
		var scans []string
		for id, on := range scanning {
			if on {
				scans = append(scans, id)
			}
		}
		qMu.Unlock()
		return map[string]any{
			"counts": statusCounts(), "running": runningJobs(), "queue": ql, "scanning": scans,
			"paused": s.Paused, "testMode": s.TestMode, "setupDone": s.SetupDone,
			"inSchedule": inSchedule(s, time.Now()), "activeFrom": s.ActiveFrom, "activeTo": s.ActiveTo,
		}, nil
	}))
	m.HandleFunc("GET /api/files", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		list := fileList(q.Get("status"), q.Get("library"))
		if s := strings.ToLower(q.Get("q")); s != "" {
			list = slices.DeleteFunc(list, func(f FileState) bool { return !strings.Contains(strings.ToLower(f.Path), s) })
		}
		if q.Get("status") == "queued" {
			qMu.Lock()
			pos := map[string]int{}
			for i, p := range queue {
				pos[p] = i
			}
			qMu.Unlock()
			sort.SliceStable(list, func(i, k int) bool { return pos[list[i].Path] < pos[list[k].Path] })
		}
		total := len(list)
		if limit > 0 && len(list) > limit {
			list = list[:limit]
		}
		return map[string]any{"files": list, "total": total}, nil
	}))
	m.HandleFunc("POST /api/files/action", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var b struct {
			Paths  []string
			Action string
			Status string // with no paths: apply to every file in this status
		}
		if err := readBody(r, &b); err != nil {
			return nil, err
		}
		if len(b.Paths) == 0 && b.Status != "" {
			for _, f := range fileList(b.Status, "") {
				b.Paths = append(b.Paths, f.Path)
			}
		}
		for _, p := range b.Paths {
			lib, _ := libraryFor(p)
			switch b.Action {
			case "retry":
				enqueue(p, lib.ID, false)
			case "now":
				unqueue(p)
				enqueue(p, lib.ID, true)
			case "ignore":
				unqueue(p)
				setFile(p, lib.ID, "ignored", "ignored by you")
			case "unignore":
				setFile(p, lib.ID, "skipped", "")
				enqueue(p, lib.ID, false)
			case "dequeue":
				unqueue(p)
				setFile(p, lib.ID, "skipped", "removed from queue")
			default:
				return nil, errors.New("unknown action")
			}
		}
		return map[string]int{"count": len(b.Paths)}, nil
	}))
	m.HandleFunc("POST /api/jobs/{id}/cancel", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if !cancelJob(r.PathValue("id")) {
			return nil, errors.New("job not running")
		}
		return nil, nil
	}))
	m.HandleFunc("GET /api/history", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 200
		}
		return recentRecords(limit, r.URL.Query().Get("status")), nil
	}))
	m.HandleFunc("GET /api/history/{id}", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		rec, ok := findRecord(r.PathValue("id"))
		if !ok {
			return nil, errors.New("not found")
		}
		return rec, nil
	}))
	m.HandleFunc("GET /api/stats", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days <= 0 || days > 365 {
			days = 30
		}
		return computeStats(days), nil
	}))

	m.HandleFunc("GET /api/nodes", api(func(w http.ResponseWriter, r *http.Request) (any, error) { return nodeList(), nil }))
	m.HandleFunc("GET /api/flows", api(func(w http.ResponseWriter, r *http.Request) (any, error) { return allFlows(), nil }))
	m.HandleFunc("GET /api/flows/{id}", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if f := getFlow(r.PathValue("id")); f != nil {
			return f, nil
		}
		return nil, errors.New("flow not found")
	}))
	m.HandleFunc("POST /api/flows", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var f Flow
		if err := readBody(r, &f); err != nil {
			return nil, err
		}
		if strings.TrimSpace(f.Name) == "" {
			return nil, errors.New("give the flow a name")
		}
		return f, saveFlow(&f)
	}))
	m.HandleFunc("DELETE /api/flows/{id}", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		return nil, deleteFlow(r.PathValue("id"))
	}))
	m.HandleFunc("POST /api/flows/test", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var b struct {
			Flow Flow
			Path string
		}
		if err := readBody(r, &b); err != nil {
			return nil, err
		}
		if fi, err := os.Stat(b.Path); err != nil || fi.IsDir() {
			return nil, errors.New("pick a video file to test with")
		}
		lib, ok := libraryFor(b.Path)
		if !ok {
			lib = Library{Path: filepath.Dir(b.Path)}
		}
		j := newJob(b.Path, lib, &b.Flow, true)
		rec := j.execute()
		return rec, nil
	}))

	m.HandleFunc("GET /api/settings", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		s := cfg()
		s.PasswordHash, s.PasswordSalt = "", ""
		return map[string]any{"settings": s, "passwordSet": cfg().PasswordHash != ""}, nil
	}))
	m.HandleFunc("POST /api/settings", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var s Settings
		if err := readBody(r, &s); err != nil {
			return nil, err
		}
		old := cfg()
		s.PasswordHash, s.PasswordSalt = old.PasswordHash, old.PasswordSalt
		s.Paused = old.Paused // a settings form opened earlier must not undo Pause/Resume
		if err := checkLibraries(s.Libraries); err != nil {
			return nil, err
		}
		for _, l := range s.Libraries {
			if fi, err := os.Stat(l.Path); err != nil || !fi.IsDir() {
				return nil, errors.New("folder not found: " + l.Path)
			}
			if getFlow(l.FlowID) == nil {
				return nil, errors.New("pick a flow for library " + l.Name)
			}
		}
		for _, t := range []string{s.ActiveFrom, s.ActiveTo} {
			if _, err := time.Parse("15:04", t); t != "" && err != nil {
				return nil, errors.New("times must look like 23:30")
			}
		}
		if err := saveSettings(s); err != nil {
			return nil, err
		}
		if old.TestMode && !s.TestMode {
			scanNow <- "" // re-check files that were only looked at in test mode
		}
		s.PasswordHash, s.PasswordSalt = "", ""
		return s, nil
	}))
	m.HandleFunc("POST /api/password", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var b struct{ Password string }
		if err := readBody(r, &b); err != nil {
			return nil, err
		}
		s := cfg()
		if b.Password == "" {
			s.PasswordHash, s.PasswordSalt = "", ""
		} else {
			if len(b.Password) < 6 {
				return nil, errors.New("use at least 6 characters")
			}
			salt := make([]byte, 16)
			rand.Read(salt)
			s.PasswordSalt = hex.EncodeToString(salt)
			s.PasswordHash = hashPassword(b.Password, s.PasswordSalt)
		}
		sessMu.Lock()
		clear(sessions)
		sessMu.Unlock()
		return nil, saveSettings(s)
	}))
	m.HandleFunc("POST /api/scan", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var b struct{ Library string }
		readBody(r, &b)
		scanNow <- b.Library
		return nil, nil
	}))
	m.HandleFunc("POST /api/pause", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var b struct{ Paused bool }
		if err := readBody(r, &b); err != nil {
			return nil, err
		}
		s := cfg()
		s.Paused = b.Paused
		if !b.Paused {
			qMu.Lock()
			failStreak = 0
			qMu.Unlock()
		}
		return nil, saveSettings(s)
	}))
	m.HandleFunc("GET /api/browse", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		dir := filepath.Clean("/" + r.URL.Query().Get("path"))
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		type ent struct {
			Name string `json:"name"`
			Dir  bool   `json:"dir"`
			Size int64  `json:"size,omitempty"`
		}
		var out []ent
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.IsDir() {
				out = append(out, ent{Name: e.Name(), Dir: true})
			} else if slices.Contains(videoExt, strings.ToLower(filepath.Ext(e.Name()))) {
				fi, _ := e.Info()
				out = append(out, ent{Name: e.Name(), Size: fi.Size()})
			}
		}
		return map[string]any{"path": dir, "entries": out}, nil
	}))
	m.HandleFunc("GET /api/hardware", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		hwMu.RLock()
		defer hwMu.RUnlock()
		return map[string]any{"encoders": encoders, "vmaf": hasVMAF, "dovi": hasDoviBSF, "ffmpeg": ffmpegVer, "detecting": detecting}, nil
	}))
	m.HandleFunc("POST /api/hardware/detect", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		detectHardware()
		return nil, nil
	}))
	m.HandleFunc("POST /api/arr/test", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var a ArrConf
		if err := readBody(r, &a); err != nil {
			return nil, err
		}
		return nil, testArr(a)
	}))
	m.HandleFunc("POST /api/discord/test", api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		var b struct{ URL string }
		readBody(r, &b)
		if b.URL == "" {
			return nil, errors.New("paste a Discord webhook URL first")
		}
		err := discordTo(b.URL, "Shrinkarr test message 👋")
		return nil, err
	}))
	return m
}

// checkLibraries rejects a folder inside another library: both would claim the same files.
func checkLibraries(libs []Library) error {
	for i, a := range libs {
		for _, b := range libs[i+1:] {
			pa, pb := filepath.Clean(a.Path)+"/", filepath.Clean(b.Path)+"/"
			if strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa) {
				return fmt.Errorf("libraries %q and %q overlap; one folder is inside the other", a.Name, b.Name)
			}
		}
	}
	return nil
}
