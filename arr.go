package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Arr struct{ Kind, URL, Key string }

var httpc = &http.Client{Timeout: 30 * time.Second}

func (a Arr) call(method, path string, body, out any) error {
	var r *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, strings.TrimRight(a.URL, "/")+path, r)
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

// notifyArrs tells whichever Radarr/Sonarr owns the file to rescan it.
func notifyArrs(path string) {
	p := mapPath(path)
	for _, a := range cfg.Arrs {
		list, cmd, idField := "/api/v3/movie", "RescanMovie", "movieId"
		if a.Kind == "sonarr" {
			list, cmd, idField = "/api/v3/series", "RescanSeries", "seriesId"
		}
		var items []struct {
			ID   int    `json:"id"`
			Path string `json:"path"`
		}
		if err := a.call("GET", list, nil, &items); err != nil {
			logf("%s: %v", a.Kind, err)
			continue
		}
		for _, it := range items {
			if it.Path != "" && strings.HasPrefix(p, strings.TrimRight(it.Path, "/")+"/") {
				if err := a.call("POST", "/api/v3/command", map[string]any{"name": cmd, idField: it.ID}, nil); err != nil {
					logf("%s: %v", a.Kind, err)
				} else {
					logf("%s: %s id %d", a.Kind, cmd, it.ID)
				}
				return
			}
		}
	}
}

// mapPath converts a container path to the path the arrs see, using PATH_MAP=from:to.
func mapPath(p string) string {
	if cfg.PathFrom != "" && strings.HasPrefix(p, cfg.PathFrom) {
		return cfg.PathTo + strings.TrimPrefix(p, cfg.PathFrom)
	}
	return p
}
