package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Library struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	FlowID      string `json:"flowId"`
	Enabled     bool   `json:"enabled"`
	ScanMinutes int    `json:"scanMinutes"`
}

type ArrConf struct {
	Kind string `json:"kind"` // radarr | sonarr
	URL  string `json:"url"`
	Key  string `json:"key"`
}

type Settings struct {
	SetupDone     bool      `json:"setupDone"`
	TestMode      bool      `json:"testMode"` // change no files
	Paused        bool      `json:"paused"`
	Workers       int       `json:"workers"`
	Hardware      string    `json:"hardware"` // auto | nvenc | qsv | vaapi | cpu
	VAAPIDevice   string    `json:"vaapiDevice"`
	TempDir       string    `json:"tempDir"`
	MinAgeMinutes int       `json:"minAgeMinutes"`
	ActiveFrom    string    `json:"activeFrom"` // "HH:MM", empty = always
	ActiveTo      string    `json:"activeTo"`
	VMAFThreads   int       `json:"vmafThreads"`
	Libraries     []Library `json:"libraries"`
	Arrs          []ArrConf `json:"arrs"`
	PathMapFrom   string    `json:"pathMapFrom"`
	PathMapTo     string    `json:"pathMapTo"`
	DiscordURL    string    `json:"discordUrl"`
	PasswordHash  string    `json:"passwordHash,omitempty"`
	PasswordSalt  string    `json:"passwordSalt,omitempty"`
	OIDCIssuer    string    `json:"oidcIssuer"`
	OIDCClientID  string    `json:"oidcClientId"`
	OIDCSecret    string    `json:"oidcSecret,omitempty"`
	OIDCAllowed   string    `json:"oidcAllowed"` // comma-separated users, emails or groups; empty = anyone the provider lets in
}

var (
	configDir  = envOr("CONFIG_DIR", "/config")
	settingsMu sync.RWMutex
	settings   = defaultSettings()
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func defaultSettings() Settings {
	return Settings{
		TestMode: true, Workers: 1, Hardware: "auto", VAAPIDevice: "/dev/dri/renderD128",
		TempDir: "/tmp", MinAgeMinutes: 10, VMAFThreads: 2,
	}
}

func cfg() Settings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	s := settings
	s.Libraries = append([]Library{}, settings.Libraries...)
	s.Arrs = append([]ArrConf{}, settings.Arrs...)
	return s
}

func loadSettings() error {
	b, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s := defaultSettings()
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	settingsMu.Lock()
	settings = s
	settingsMu.Unlock()
	return nil
}

func saveSettings(s Settings) error {
	if s.Workers < 1 {
		s.Workers = 1
	}
	for i := range s.Libraries {
		l := &s.Libraries[i]
		l.Path = filepath.Clean(strings.TrimSpace(l.Path))
		if l.ID == "" {
			l.ID = randID()
		}
		if l.ScanMinutes <= 0 {
			l.ScanMinutes = 30
		}
	}
	settingsMu.Lock()
	settings = s
	settingsMu.Unlock()
	return writeJSON(filepath.Join(configDir, "settings.json"), s)
}

// writeJSON writes atomically so a crash never leaves a half-written file.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
