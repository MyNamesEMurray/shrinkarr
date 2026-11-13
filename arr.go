package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
		list, cmd, idField := arrKind(a)
		var items []struct {
			ID   int    `json:"id"`
			Path string `json:"path"`
		}
		if err := arrCall(a, "GET", list, nil, &items); err != nil {
			msgs = append(msgs, fmt.Sprintf("%s: %v", a.Kind, err))
			continue
		}
		for _, it := range items {
			if it.Path != "" && strings.HasPrefix(p, strings.TrimRight(it.Path, "/")+"/") {
				if err := arrCall(a, "POST", "/api/v3/command", map[string]any{"name": cmd, idField: it.ID}, nil); err != nil {
					return fmt.Sprintf("%s: %v", a.Kind, err)
				}
				return fmt.Sprintf("%s: %s id %d", a.Kind, cmd, it.ID)
			}
		}
		msgs = append(msgs, fmt.Sprintf("%s: no item owns %s", a.Kind, p))
	}
	return strings.Join(msgs, "; ")
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
