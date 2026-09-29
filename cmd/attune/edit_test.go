package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedEditor replaces the editor with a sequence of rewrites; nil keeps
// the file as it is. It records the temporary directory it was shown.
func scriptedEditor(t *testing.T, dir *string, rewrites ...func(current string) string) func(string) error {
	t.Helper()
	call := 0
	return func(path string) error {
		*dir = filepath.Dir(path)
		if call >= len(rewrites) {
			t.Fatalf("editor called %d times, scripted %d", call+1, len(rewrites))
		}
		fn := rewrites[call]
		call++
		if fn == nil {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(fn(string(b))), 0o600)
	}
}

func TestEditSavesAValidatedVersion(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	var dir string
	h.app.editor = scriptedEditor(t, &dir, func(cur string) string { return cur + "# edited\n" })
	if out := h.mustRun("edit", "app.yaml"); out != line("updated", "app.yaml") {
		t.Fatalf("edit output %q", out)
	}
	if rec := h.lastSave(); rec.Action != "edit app.yaml" || rec.Generation != h.generation() {
		t.Fatalf("record after edit: %+v", rec)
	}
	if got := h.mustRun("cat", "app.yaml"); got != h.read(h.spec("app.yaml"))+"# edited\n" {
		t.Fatalf("cat after edit %q", got)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("temporary directory %s still exists", dir)
	}
	if !strings.HasPrefix(filepath.Base(dir), "attune-edit-") {
		t.Fatalf("editor was shown %s, not a private temp directory", dir)
	}
	st, err := h.app.openStore(h.store)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	all, _ := st.Entries()
	if n, err := st.VersionCount(entryFor(all, "app.yaml").ID); err != nil || n != 2 {
		t.Fatalf("versions after edit %d %v, want 2", n, err)
	}
}

func TestEditReopensOnInvalidAndAbortsOnUnchanged(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	var dir string
	h.app.editor = scriptedEditor(t, &dir, func(string) string { return "kind: [unterminated\n" }, nil)
	out, errs := h.mustFail(1, "edit", "app.yaml")
	if !strings.Contains(errs, "parse app.yaml") || !strings.Contains(errs, "quit without changes to abort") {
		t.Fatalf("invalid edit stderr %q", errs)
	}
	if out != "edit aborted; nothing saved\n" {
		t.Fatalf("invalid edit stdout %q", out)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("temporary directory %s still exists", dir)
	}
	if got := h.mustRun("cat", "app.yaml"); got != h.read(h.spec("app.yaml")) {
		t.Fatal("aborted edit changed the stored content")
	}

	h.app.editor = scriptedEditor(t, &dir, func(string) string { return "provider: azure\nbogus: 1\n" }, nil)
	if _, errs := h.mustFail(1, "edit", "attune.yaml"); !strings.Contains(errs, "bogus") {
		t.Fatalf("invalid configuration edit %q", errs)
	}

	h.app.editor = scriptedEditor(t, &dir, nil)
	if out := h.mustRun("edit", "dns.yaml"); out != line("unchanged", "dns.yaml") {
		t.Fatalf("unchanged edit %q", out)
	}
	h.app.editor = scriptedEditor(t, &dir, func(cur string) string {
		return strings.Replace(cur, "kind: dnsRecordSet", "kind: dnsRecordSet\n# note", 1)
	})
	if out := h.mustRun("edit", "dns.yaml"); out != line("updated", "dns.yaml") {
		t.Fatalf("second valid edit %q", out)
	}

	h.app.isTerminal = func() bool { return false }
	if _, errs := h.mustFail(1, "edit", "dns.yaml"); !strings.Contains(errs, "needs a terminal") {
		t.Fatalf("edit without a terminal %q", errs)
	}
	h.app.isTerminal = func() bool { return true }
	if _, errs := h.mustFail(1, "edit", "nope.yaml"); !strings.Contains(errs, "not registered") {
		t.Fatalf("edit unknown %q", errs)
	}
	if _, errs := h.mustFail(2, "edit"); !strings.Contains(errs, "usage") {
		t.Fatalf("edit without a name %q", errs)
	}
}
