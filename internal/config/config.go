// Package config parses the sandbox-ext config knob into a strongly-typed
// sandbox list — the Go twin of the worker-extension's src/config.ts.
package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Knob names — the single source of truth for the manifest + readers.
const KnobSandboxes = "sandboxes"

// Sandbox is one configured easyworker.
type Sandbox struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Parse parses the `sandboxes` JSON list, dropping invalid entries (a bad row
// must never take down the extension).
func Parse(raw string) []Sandbox {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var rows []Sandbox
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil
	}
	out := make([]Sandbox, 0, len(rows))
	for _, s := range rows {
		s.Name = strings.TrimSpace(s.Name)
		s.URL = strings.TrimSpace(s.URL)
		s.Token = strings.TrimSpace(s.Token)
		if s.Name == "" || s.URL == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Names returns every configured sandbox name (for error text / discovery).
func Names(list []Sandbox) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

// Select resolves a sandbox by name. `wanted` empty selects the first; an
// unknown name is an error listing the known ones.
func Select(list []Sandbox, wanted string) (Sandbox, error) {
	if len(list) == 0 {
		return Sandbox{}, fmt.Errorf("no sandboxes configured (set the `%s` config)", KnobSandboxes)
	}
	if wanted == "" {
		return list[0], nil
	}
	for _, s := range list {
		if s.Name == wanted {
			return s, nil
		}
	}
	return Sandbox{}, fmt.Errorf("unknown sandbox %q (known: %s)", wanted, strings.Join(Names(list), ", "))
}
