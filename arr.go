package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

var httpc = &http.Client{Timeout: 30 * time.Second}

func arrCall(a ArrConf, method, path string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, strings.TrimRight(a.URL, "/")+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", a.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func arrKind(a ArrConf) (list, cmd, idField string) {
	if a.Kind == "sonarr" {
		return "/api/v3/series", "RescanSeries", "seriesId"
	}
	return "/api/v3/movie", "RescanMovie", "movieId"
}

// notifyArrs tells whichever Radarr/Sonarr owns the file to rescan it. Returns a log line.
func notifyArrs(path string) string {
	s := cfg()
	if len(s.Arrs) == 0 {
		return "no Radarr/Sonarr configured"
	}
	p := path
	if s.PathMapFrom != "" && strings.HasPrefix(p, s.PathMapFrom) {
		p = s.PathMapTo + strings.TrimPrefix(p, s.PathMapFrom)
	}
	var msgs []string
	for _, a := range s.Arrs {
		_, cmd, idField := arrKind(a)
		id, fresh, err := arrOwner(a, p, false)
		if err == nil && id == 0 && !fresh {
			id, _, err = arrOwner(a, p, true) // maybe added since the list was cached
		}
		switch {
		case err != nil:
			msgs = append(msgs, fmt.Sprintf("%s: %v", a.Kind, err))
		case id == 0:
			msgs = append(msgs, fmt.Sprintf("%s: no item owns %s", a.Kind, p))
		default:
			if err := arrCall(a, "POST", "/api/v3/command", map[string]any{"name": cmd, idField: id}, nil); err != nil {
				return fmt.Sprintf("%s: %v", a.Kind, err)
			}
			return fmt.Sprintf("%s: %s id %d", a.Kind, cmd, id)
		}
	}
	return strings.Join(msgs, "; ")
}

type arrItem struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
}

var (
	arrMu    sync.Mutex
	arrCache = map[string]struct {
		items []arrItem
		at    time.Time
	}{}
)

// arrOwner returns the id of the movie/series whose folder holds path, or 0,
// and whether the list was just fetched. The list is cached for 10 minutes: it can be several MB.
func arrOwner(a ArrConf, path string, refresh bool) (id int, fresh bool, err error) {
	key := a.Kind + "|" + a.URL
	arrMu.Lock()
	c, ok := arrCache[key]
	arrMu.Unlock()
	if refresh || !ok || time.Since(c.at) > 10*time.Minute {
		list, _, _ := arrKind(a)
		var items []arrItem
		if err := arrCall(a, "GET", list, nil, &items); err != nil {
			return 0, false, err
		}
		c.items, c.at, fresh = items, time.Now(), true
		arrMu.Lock()
		arrCache[key] = c
		arrMu.Unlock()
	}
	for _, it := range c.items {
		if it.Path != "" && strings.HasPrefix(path, strings.TrimRight(it.Path, "/")+"/") {
			return it.ID, fresh, nil
		}
	}
	return 0, fresh, nil
}

// testArr checks URL and API key for the Settings page.
func testArr(a ArrConf) error {
	var st struct {
		Version string `json:"version"`
	}
	if err := arrCall(a, "GET", "/api/v3/system/status", nil, &st); err != nil {
		return err
	}
	if st.Version == "" {
		return errors.New("connected, but that doesn't look like Radarr/Sonarr")
	}
	return nil
}

func discord(msg string) error { return discordTo(cfg().DiscordURL, msg) }

func discordTo(url, msg string) error {
	if url == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]string{"content": msg})
	resp, err := httpc.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return errors.New(resp.Status)
	}
	return nil
}
