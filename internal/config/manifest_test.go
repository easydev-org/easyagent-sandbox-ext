package config

import (
	"os"
	"testing"

	"github.com/easydev-org/easybus-sdk/manifest"
)

// TestSandboxesIsAStructuredKnob is the regression test for a knob that every
// tool in this extension requires.
//
// `sandboxes` was declared `type: string` with a description telling the user to
// type JSON by hand. Because it is `required_config` on every tool, a typo in
// that blob disabled the whole extension, and the console could only render a
// textarea — no per-item validation, no way to see what a valid entry looks like.
//
// Asserting the SHAPE (not just "it parses") is the point: the console renders a
// list editor only when type=item_type+fields survive the manifest hop.
func TestSandboxesIsAStructuredKnob(t *testing.T) {
	raw, err := os.ReadFile("../../manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m, err := manifest.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	var found bool
	for _, c := range m.Config {
		if c.Name != "sandboxes" {
			continue
		}
		found = true
		if c.Type != "list" {
			t.Errorf("type = %q, want list (a JSON string cannot be validated item by item)", c.Type)
		}
		if c.ItemType != "object" {
			t.Errorf("item_type = %q, want object", c.ItemType)
		}
		if c.MinItems < 1 {
			t.Errorf("min_items = %d, want >= 1 (a sandbox list with no entries is unusable)", c.MinItems)
		}
		if len(c.Fields) == 0 {
			t.Fatal("fields is empty: the console would fall back to a JSON textarea")
		}
		names := map[string]bool{}
		for _, f := range c.Fields {
			names[f.Name] = true
		}
		for _, want := range []string{"name", "url", "token", "os"} {
			if !names[want] {
				t.Errorf("field %q missing: the form cannot show it", want)
			}
		}
	}
	if !found {
		t.Fatal("no `sandboxes` knob in the manifest")
	}
}
