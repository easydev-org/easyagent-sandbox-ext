package tools

import (
	"bytes"
	"os"
	"testing"
)

// TestEmbeddedManifestMatchesRoot guards a duplicated file that drifted once.

// `//go:embed manifest.yaml` resolves relative to cmd/sandbox-ext, so THAT copy is
// what ships — not the root file people edit. Nothing at runtime reveals which one
// a binary was built from.
func TestEmbeddedManifestMatchesRoot(t *testing.T) {
	root, err := os.ReadFile("../../manifest.yaml")
	if err != nil {
		t.Fatalf("read root manifest: %v", err)
	}
	embedded, err := os.ReadFile("../../cmd/sandbox-ext/manifest.yaml")
	if err != nil {
		t.Fatalf("read embedded manifest: %v", err)
	}
	if !bytes.Equal(root, embedded) {
		t.Fatal("manifest.yaml and cmd/sandbox-ext/manifest.yaml differ; the embedded copy is what ships")
	}
}
