package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	configDir, _ = os.MkdirTemp("", "shrinkarr-cfg-")
	loadTemplates()
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		detectHardware()
	}
	code := m.Run()
	os.RemoveAll(configDir)
	os.Exit(code)
}

func TestTemplatesValid(t *testing.T) {
	if len(builtinFlows) < 5 || builtinFlows[0].ID != "tpl-space" {
		t.Fatalf("templates: %d, first %s", len(builtinFlows), builtinFlows[0].ID)
	}
	f := Flow{Nodes: []Node{{ID: "1", Type: "start"}, {ID: "2", Type: "run_ffmpeg"}},
		Edges: []Edge{{From: "1", Out: 1, To: "2"}, {From: "1", Out: 1, To: "2"}}}
	if f.validate() == nil {
		t.Fatal("double-connected output should be rejected")
	}
}

func TestSchedule(t *testing.T) {
	at := func(hm string) time.Time { tm, _ := time.Parse("15:04", hm); return tm }
	s := Settings{ActiveFrom: "23:00", ActiveTo: "07:00"}
	if !inSchedule(s, at("01:30")) || inSchedule(s, at("12:00")) || !inSchedule(Settings{}, at("12:00")) {
		t.Fatal("overnight window wrong")
	}
}

func TestSearchQuality(t *testing.T) {
	q, s, _ := searchQuality(18, 34, 95, func(q int) (float64, error) { return 100 - float64(q-18), nil })
	if q != 23 || s != 95 {
		t.Fatalf("got %d %v", q, s)
	}
}

func TestFitBox(t *testing.T) {
	s := Stream{Width: 3840, Height: 1600}
	if w, h := s.fitBox(1080); w != 1920 || h != 800 {
		t.Fatalf("scope 4K -> %dx%d", w, h)
	}
	if w, _ := (&Stream{Width: 1280, Height: 720}).fitBox(1080); w != 0 {
		t.Fatal("should not upscale")
	}
	if s.effHeight() != 2160 {
		t.Fatal("scope 4K should count as 2160p")
	}
}

// Streams with no codec_name (Tdarr's ffprobe bug), PGS, TrueHD 7.1 and DV into MP4.
func TestBuildArgsMP4(t *testing.T) {
	hasDoviBSF = true
	j := &Job{Cur: "in.mkv"}
	j.Probe.Format.FormatName = "matroska,webm"
	v := Stream{Index: 1, CodecType: "video", CodecName: "hevc"}
	v.SideData = append(v.SideData, struct {
		Type string `json:"side_data_type"`
	}{"DOVI configuration record"})
	j.Probe.Streams = []Stream{{Index: 0, CodecType: "attachment"}, v,
		{Index: 2, CodecType: "audio", CodecName: "truehd", Channels: 8},
		{Index: 3, CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle"},
		{Index: 4, CodecType: "subtitle", CodecName: "subrip"}, {Index: 5, CodecType: "data"}}
	j.Cmd.Container = "mp4"
	j.Cmd.Video.StripDV = true
	j.Cmd.Audio.KeepCodecs = []string{"aac", "ac3", "eac3"}
	args, changed, err := j.buildArgs(buildOpts{out: "o.mp4"})
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	got := strings.Join(args, " ")
	want := "-i in.mkv -map 0:1 -c:v copy -tag:v hvc1 -bsf:v dovi_rpu=strip=1 -map 0:2 -c:1 eac3 -b:1 640k -ac:1 6 -map 0:4 -c:2 mov_text"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("\n got %s\nwant %s…", got, want)
	}
}

func TestNothingToDo(t *testing.T) {
	j := &Job{Cur: "in.mp4"}
	j.Probe.Format.FormatName = "mov,mp4,m4a"
	j.Probe.Streams = []Stream{{Index: 0, CodecType: "video", CodecName: "hevc", CodecTag: "hvc1"}, {Index: 1, CodecType: "audio", CodecName: "eac3"}}
	j.Cmd.Container = "mp4"
	if _, changed, _ := j.buildArgs(buildOpts{out: "o.mp4"}); changed {
		t.Fatal("HEVC MP4 with eac3 should be nothing to do")
	}
}

func fixture(t *testing.T, dir, name, vcodec, acodec string) string {
	t.Helper()
	srt := filepath.Join(dir, "s.srt")
	os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n"), 0o644)
	p := filepath.Join(dir, name)
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=d=4:s=640x360:r=24",
		"-f", "lavfi", "-i", "sine=d=4", "-i", srt, "-map", "0", "-map", "1", "-map", "2",
		"-c:v", vcodec, "-b:v", "3M", "-c:a", acodec, "-ac", "2", "-c:s", "srt", p).CombinedOutput()
	if err != nil {
		t.Skipf("cannot build fixture: %s", out)
	}
	return p
}

func runTemplate(t *testing.T, id, path string, test bool) Record {
	t.Helper()
	settings.Hardware = "cpu"
	settings.TempDir = t.TempDir()
	j := newJob(path, Library{ID: "lib", Path: filepath.Dir(path)}, getFlow(id), test)
	return j.execute()
}

func codecs(t *testing.T, path string) string {
	p, err := ffprobe(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var c []string
	for _, s := range p.Streams {
		c = append(c, s.CodecName)
	}
	return strings.Join(c, ",")
}

func TestE2E(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	if !encoders[3].supports("hevc") {
		t.Skip("no libx265")
	}
	dir := t.TempDir()

	t.Run("test mode changes nothing", func(t *testing.T) {
		src := fixture(t, dir, "a.mkv", "mpeg4", "flac")
		before, _ := os.Stat(src)
		r := runTemplate(t, "tpl-space", src, true)
		after, err := os.Stat(src)
		if r.Status != "skipped" || !strings.Contains(r.Detail, "Test mode") || err != nil || !after.ModTime().Equal(before.ModTime()) {
			t.Fatalf("%s: %s", r.Status, r.Detail)
		}
		if len(r.Commands) == 0 || !strings.Contains(r.Commands[0], "libx265") {
			t.Fatalf("commands %v", r.Commands)
		}
	})

	t.Run("space saver mpeg4 to hevc", func(t *testing.T) {
		src := fixture(t, dir, "b.mkv", "mpeg4", "flac")
		r := runTemplate(t, "tpl-space", src, false)
		if r.Status != "done" {
			t.Fatalf("%s: %s\n%s", r.Status, r.Detail, r.Log)
		}
		if got := codecs(t, src); got != "hevc,flac,subrip" {
			t.Fatalf("streams %s", got)
		}
		if r.NewSize >= r.OrigSize {
			t.Fatalf("no saving %d >= %d", r.NewSize, r.OrigSize)
		}
		left, _ := filepath.Glob(filepath.Join(dir, ".shrinkarr-*"))
		if len(left) > 0 {
			t.Fatalf("temp files left: %v", left)
		}
	})

	t.Run("apple tv remux hevc mkv to mp4", func(t *testing.T) {
		src := fixture(t, dir, "c.mkv", "libx265", "flac")
		r := runTemplate(t, "tpl-appletv", src, false)
		if r.Status != "done" {
			t.Fatalf("%s: %s\n%s", r.Status, r.Detail, r.Log)
		}
		mp4 := filepath.Join(dir, "c.mp4")
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Fatal("original mkv still there")
		}
		if got := codecs(t, mp4); got != "hevc,eac3,mov_text" {
			t.Fatalf("streams %s", got)
		}
		r2 := runTemplate(t, "tpl-appletv", mp4, false)
		if r2.Status != "skipped" || r2.Detail != "nothing to do" {
			t.Fatalf("second pass should be a no-op, got %s: %s", r2.Status, r2.Detail)
		}
	})

	t.Run("compat adds stereo aac", func(t *testing.T) {
		src := fixture(t, dir, "d.mkv", "libx265", "ac3")
		exec.Command("ffmpeg", "-v", "error", "-y", "-i", src, "-map", "0:v", "-map", "0:a", "-c:v", "copy", "-c:a", "ac3", "-ac", "6", filepath.Join(dir, "d6.mkv")).Run()
		r := runTemplate(t, "tpl-compat", filepath.Join(dir, "d6.mkv"), false)
		if r.Status != "done" {
			t.Fatalf("%s: %s\n%s", r.Status, r.Detail, r.Log)
		}
		if got := codecs(t, filepath.Join(dir, "d6.mp4")); got != "h264,aac,aac" {
			t.Fatalf("streams %s", got)
		}
	})

	t.Run("move original", func(t *testing.T) {
		src := fixture(t, dir, "e.mkv", "mpeg4", "aac")
		keep := filepath.Join(t.TempDir(), "orig")
		f := *getFlow("tpl-space")
		f.Nodes = append([]Node(nil), f.Nodes...)
		for i := range f.Nodes {
			if f.Nodes[i].Type == "replace" {
				f.Nodes[i].Params = map[string]any{"original": "move", "folder": keep}
			}
		}
		settings.TempDir = t.TempDir()
		j := newJob(src, Library{ID: "lib", Path: dir}, &f, false)
		if r := j.execute(); r.Status != "done" {
			t.Fatalf("%s: %s", r.Status, r.Detail)
		}
		if _, err := os.Stat(filepath.Join(keep, "e.mkv")); err != nil {
			t.Fatal("original not moved:", err)
		}
	})
}

func resetState(t *testing.T) {
	t.Helper()
	storeMu.Lock()
	files = map[string]*FileState{}
	storeMu.Unlock()
	qMu.Lock()
	queue, running, failStreak = nil, map[string]*Job{}, 0
	qMu.Unlock()
}

// The scan rule decides what gets (re)processed; a mistake re-encodes or skips a whole library.
func TestScanRules(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	mk := func(name string, age time.Time) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(name), 0o644)
		os.Chtimes(p, age, age)
		return p
	}
	known := func(p, status string, test bool) {
		fi, _ := os.Stat(p)
		files[p] = &FileState{Path: p, Size: fi.Size(), Mtime: fi.ModTime().Unix(), Status: status, Test: test}
	}
	unchanged := mk("unchanged.mkv", old)
	known(unchanged, "done", false)
	changed := mk("changed.mkv", old)
	known(changed, "done", false)
	files[changed].Size = 1
	ignored := mk("ignored.mkv", old)
	known(ignored, "ignored", false)
	tested := mk("tested.mkv", old)
	known(tested, "skipped", true)
	removed := mk("removed.mkv", old)
	known(removed, "skipped", false) // what "Clear queue" leaves behind
	fresh := mk("copying.mkv", time.Now())
	newFile := mk("new.mkv", old)
	mk("notes.txt", old)

	settings.MinAgeMinutes, settings.TestMode = 10, false
	scanLibrary(Library{ID: "l", Name: "t", Path: dir})
	got := map[string]bool{}
	for _, p := range queue {
		got[p] = true
	}
	want := map[string]bool{changed: true, tested: true, newFile: true}
	for _, p := range []string{unchanged, changed, ignored, tested, removed, fresh, newFile} {
		if got[p] != want[p] {
			t.Errorf("%s queued=%v, want %v", filepath.Base(p), got[p], want[p])
		}
	}
	if len(queue) != 3 {
		t.Errorf("queue %v", queue)
	}

	// With test mode on, a test-mode result counts as known.
	resetState(t)
	known(tested, "skipped", true)
	settings.TestMode = true
	scanLibrary(Library{ID: "l", Name: "t", Path: dir})
	for _, p := range queue {
		if p == tested {
			t.Error("test-mode result requeued while test mode is still on")
		}
	}
	settings.TestMode = false
}

func TestCSRFBlocked(t *testing.T) {
	h := routes()
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
		req := httptest.NewRequest("POST", "/api/pause", strings.NewReader(`{"paused":true}`))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%q: got %d, want 415", ct, rec.Code)
		}
	}
	req := httptest.NewRequest("POST", "/api/pause", strings.NewReader(`{"paused":false}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("json request: %d %s", rec.Code, rec.Body)
	}
}

func TestFailStreakPauses(t *testing.T) {
	resetState(t)
	settings.Paused = false
	j := &Job{Path: "/x.mkv", Lib: Library{ID: "l"}}
	fail := func(detail string) { finish(j, Record{ID: randID(), Path: j.Path, Status: "failed", Detail: detail}) }
	for i := 0; i < 10; i++ {
		fail("cancelled") // cancels are the user's doing, not a broken setup
	}
	if cfg().Paused {
		t.Fatal("paused by cancels")
	}
	for i := 0; i < maxFailStreak-1; i++ {
		fail("ffmpeg failed")
	}
	finish(j, Record{ID: randID(), Path: j.Path, Status: "done"})
	for i := 0; i < maxFailStreak-1; i++ {
		fail("ffmpeg failed")
	}
	if cfg().Paused {
		t.Fatal("a success in between should reset the count")
	}
	fail("ffmpeg failed")
	if !cfg().Paused {
		t.Fatal("not paused after 5 failures in a row")
	}
	settings.Paused = false
}

func TestIgnoreWhileRunning(t *testing.T) {
	resetState(t)
	p := filepath.Join(t.TempDir(), "a.mkv")
	os.WriteFile(p, []byte("x"), 0o644)
	setFile(p, "l", "ignored", "ignored by you")
	finish(&Job{Path: p, Lib: Library{ID: "l"}}, Record{ID: "r", Path: p, Status: "skipped", Detail: "nothing to do"})
	if files[p].Status != "ignored" {
		t.Fatalf("status %s", files[p].Status)
	}
}

func TestOverlappingLibraries(t *testing.T) {
	if checkLibraries([]Library{{Name: "a", Path: "/media"}, {Name: "b", Path: "/media/tv"}}) == nil {
		t.Fatal("nested libraries accepted")
	}
	if checkLibraries([]Library{{Name: "a", Path: "/media/tv"}, {Name: "b", Path: "/media/tv2"}}) != nil {
		t.Fatal("/media/tv and /media/tv2 don't overlap")
	}
}

func TestArrListCached(t *testing.T) {
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gets++
			w.Write([]byte(`[{"id":7,"path":"/movies/Up (2009)"}]`))
		}
	}))
	defer srv.Close()
	settings.Arrs = []ArrConf{{Kind: "radarr", URL: srv.URL}}
	defer func() { settings.Arrs = nil }()
	for i := 0; i < 3; i++ {
		if msg := notifyArrs("/movies/Up (2009)/Up.mkv"); !strings.Contains(msg, "id 7") {
			t.Fatal(msg)
		}
	}
	if gets != 1 {
		t.Fatalf("list fetched %d times, want 1", gets)
	}
	notifyArrs("/movies/New (2026)/New.mkv") // unknown: refetch once in case it was just added
	if gets != 2 {
		t.Fatalf("unknown path: %d fetches, want 2", gets)
	}
}

// Full OIDC round trip against a fake provider: discovery, PKCE, token, userinfo, allow list, session.
func TestOIDCLogin(t *testing.T) {
	var challenge string
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/o/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"issuer":"%[1]s/o","authorization_endpoint":"%[1]s/authorize","token_endpoint":"%[1]s/token","userinfo_endpoint":"%[1]s/userinfo"}`, idp.URL)
		case "/token":
			r.ParseForm()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.Form.Get("client_secret") != "sec" || r.Form.Get("code") != "c1" {
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Write([]byte(`{"access_token":"at"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer at" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"sub":"1","preferred_username":"eric","groups":["media-admins"]}`))
		}
	}))
	defer idp.Close()
	settings.OIDCIssuer, settings.OIDCClientID, settings.OIDCSecret = idp.URL+"/o", "shrinkarr", "sec"
	defer func() { settings.OIDCIssuer, settings.OIDCAllowed = "", "" }()
	h := routes()

	login := func(allowed string) (*httptest.ResponseRecorder, string) {
		settings.OIDCAllowed = allowed
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "http://sk.local/api/oidc/login", nil))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		q := loc.Query()
		challenge = q.Get("code_challenge")
		if q.Get("redirect_uri") != "http://sk.local/api/oidc/callback" || q.Get("code_challenge_method") != "S256" {
			t.Fatalf("auth redirect %s", loc)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/oidc/callback?code=c1&state="+q.Get("state"), nil))
		return rec, q.Get("state")
	}

	// Not logged in: API is locked.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/status", nil))
	if rec.Code != 401 {
		t.Fatalf("status without login: %d", rec.Code)
	}

	rec, state := login("someone-else, media-admins")
	if rec.Code != http.StatusFound || len(rec.Result().Cookies()) == 0 {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != 200 || sessionUser(req) != "eric" {
		t.Fatalf("after login: %d user %q", rec2.Code, sessionUser(req))
	}

	// The same state can't be replayed.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/oidc/callback?code=c1&state="+state, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("replayed state: %d", rec.Code)
	}

	if rec, _ := login("bob"); rec.Code != http.StatusForbidden {
		t.Fatalf("user not on allow list got %d", rec.Code)
	}
}
