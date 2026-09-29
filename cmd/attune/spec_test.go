package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestFixtureLoadsEveryKind(t *testing.T) {
	bundle, err := loadDir(t, "testdata/specs")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if bundle.Len() != 6 {
		t.Errorf("bundle.Len() = %d, want 6", bundle.Len())
	}
}

func TestDirectoryObjectIdRequiresExactUuidShape(t *testing.T) {
	valid := []string{
		"00000000-0000-0000-0000-000000000001",
		"ABCDEF00-1234-5678-9ABC-DEF012345678",
	}
	for _, v := range valid {
		if !isDirectoryObjectID(v) {
			t.Errorf("isDirectoryObjectID(%q) = false, want true", v)
		}
	}
	invalid := []string{
		"synthetic-principal",
		"000000000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-00000000000z",
	}
	for _, v := range invalid {
		if isDirectoryObjectID(v) {
			t.Errorf("isDirectoryObjectID(%q) = true, want false", v)
		}
	}
}

func TestLoadSourcesParsesNamedContents(t *testing.T) {
	entries, err := os.ReadDir("testdata/specs")
	if err != nil {
		t.Fatal(err)
	}
	var sources []specSource
	for _, e := range entries {
		content, err := os.ReadFile(filepath.Join("testdata/specs", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, specSource{name: e.Name(), content: content})
	}
	bundle, err := loadSources(sources)
	if err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if bundle.Len() != 6 {
		t.Errorf("bundle.Len() = %d, want 6", bundle.Len())
	}
	dup := append(slices.Clone(sources), specSource{name: "dup/" + sources[0].name, content: sources[0].content})
	if _, err := loadSources(dup); err == nil || !strings.Contains(err.Error(), "duplicate resource key") {
		t.Errorf("duplicate key: got %v", err)
	}
	if _, err := loadSources([]specSource{{name: "broken.yaml", content: []byte("kind: [")}}); err == nil || !strings.Contains(err.Error(), "parse broken.yaml") {
		t.Errorf("parse error names the source: got %v", err)
	}
}

// loadDir parses every spec file under dir the way add DIR and validate do.
func loadDir(t *testing.T, dir string) (*Bundle, error) {
	t.Helper()
	var paths []string
	if err := collectSpecPaths(dir, &paths); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var sources []specSource
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, specSource{name: p, content: b})
	}
	return loadSources(sources)
}
