package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderedDir parses the summary line and registers the directory for cleanup.
func renderedDir(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	last := lines[len(lines)-1]
	_, after, ok := strings.Cut(last, " into ")
	if !strings.HasPrefix(last, "rendered ") || !ok {
		t.Fatalf("no summary line: %q", out)
	}
	t.Cleanup(func() { os.RemoveAll(after) })
	return after
}

func TestRenderWritesTheSpecsAndRoundTrips(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	out := h.mustRun("render")
	if !strings.Contains(out, line("rendered", "attune.yaml")) || !strings.Contains(out, line("rendered", "app.yaml")) {
		t.Fatalf("render output %q", out)
	}
	dir := renderedDir(t, out)
	for _, rel := range []string{"attune.yaml", "app.yaml", "dns.yaml", "MANIFEST.txt"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("%s not rendered: %v", rel, err)
		}
	}
	if got := h.read(filepath.Join(dir, "app.yaml")); got != h.read(h.spec("app.yaml")) {
		t.Fatal("rendered app.yaml differs from the stored content")
	}
	if _, err := os.Stat(filepath.Join(dir, "versions")); err == nil {
		t.Fatal("versions/ rendered without -a")
	}
	manifest := h.read(filepath.Join(dir, "MANIFEST.txt"))
	if !strings.Contains(manifest, "store: "+h.store) || !strings.Contains(manifest, "app.yaml\t1\t") {
		t.Fatalf("manifest %q", manifest)
	}

	second := filepath.Join(h.root, "synced", "second.store")
	if code, _, errs := h.runRaw("init", "-N", "-t", second); code != 0 {
		t.Fatalf("second init: %q", errs)
	}
	if code, _, errs := h.runRaw("add", dir, "-t", second); code != 0 {
		t.Fatalf("add rendered tree: %q", errs)
	}
	_, out, _ = h.runRaw("ls", "-t", second)
	if got := lsNames(out); strings.Join(got, ",") != strings.Join(storeNames, ",") {
		t.Fatalf("round trip names %v, want %v", got, storeNames)
	}

	out = h.mustRun("render", "-a")
	dir = renderedDir(t, out)
	versions, err := os.ReadDir(filepath.Join(dir, "versions", "app.yaml"))
	if err != nil || len(versions) != 1 || !strings.HasPrefix(versions[0].Name(), "1-") {
		t.Fatalf("versions of app.yaml: %v %v", versions, err)
	}

	target := filepath.Join(h.root, "out")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "junk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errs := h.mustFail(1, "render", "-o", target); !strings.Contains(errs, "not empty") {
		t.Fatalf("render into a non-empty directory: %q", errs)
	}
	if out := h.mustRun("render", "-o", target, "-f"); !strings.Contains(out, "into "+target) {
		t.Fatalf("render -o -f %q", out)
	}
	if _, err := os.Stat(filepath.Join(target, "attune.yaml")); err != nil {
		t.Fatal("render -o -f wrote nothing")
	}
}
