package main

import (
	"cmp"
	"embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"slices"
)

//go:embed templates/*.json
var templateFS embed.FS

func loadTemplates() {
	entries, _ := templateFS.ReadDir("templates")
	for _, e := range entries {
		b, _ := templateFS.ReadFile("templates/" + e.Name())
		var f Flow
		if err := json.Unmarshal(b, &f); err != nil {
			log.Fatalf("template %s: %v", e.Name(), err)
		}
		if err := f.validate(); err != nil {
			log.Fatalf("template %s: %v", e.Name(), err)
		}
		f.BuiltIn = true
		builtinFlows = append(builtinFlows, &f)
	}
	order := []string{"tpl-space", "tpl-appletv", "tpl-remux", "tpl-compat", "tpl-av1"}
	slices.SortStableFunc(builtinFlows, func(a, b *Flow) int {
		return cmp.Compare(slices.Index(order, a.ID), slices.Index(order, b.ID))
	})
}

func main() {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		log.Fatal(err)
	}
	loadTemplates()
	if err := loadSettings(); err != nil {
		log.Fatalf("settings.json: %v", err)
	}
	if err := loadStore(); err != nil {
		log.Fatalf("state: %v", err)
	}
	go detectHardware()
	go scanner()
	go dispatcher()
	addr := envOr("LISTEN", ":8265")
	log.Printf("Shrinkarr listening on %s, config in %s", addr, configDir)
	log.Fatal(http.ListenAndServe(addr, routes()))
}
