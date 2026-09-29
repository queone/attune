package main

import (
	"strings"
	"testing"
)

func TestKeyLifecycle(t *testing.T) {
	h := newHarness(t)
	h.mustRun("init", "-N")
	out := h.mustRun("key", "show")
	for _, want := range []string{"store: " + h.store + " (flag)", "key id: ", "login keychain: present", "store opens: yes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("key show lacks %q: %q", want, out)
		}
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("key show must not print secrets: %q", out)
	}

	h.app.readLine = func(string) (string, error) { return "n", nil }
	if out, _ := h.mustFail(1, "key", "rm"); !strings.Contains(out, "nothing deleted") {
		t.Fatalf("key rm answered n: %q", out)
	}
	if len(h.keys.Keys) != 1 {
		t.Fatal("key rm answered n deleted the key")
	}
	h.app.isTerminal = func() bool { return false }
	if _, errs := h.mustFail(1, "key", "rm"); !strings.Contains(errs, "needs a terminal") {
		t.Fatalf("key rm without terminal: %q", errs)
	}
	h.app.isTerminal = func() bool { return true }

	h.app.readLine = func(string) (string, error) { return "Y", nil }
	if out := h.mustRun("key", "rm"); !strings.Contains(out, "keychain item deleted") {
		t.Fatalf("key rm answered y: %q", out)
	}
	if len(h.keys.Keys) != 0 {
		t.Fatal("key rm answered y left the key")
	}
	if _, errs := h.mustFail(1, "ls"); !strings.Contains(errs, "run `attune init`") {
		t.Fatalf("ls after key rm: %q", errs)
	}
	if out, _ := h.mustFail(1, "key", "show"); !strings.Contains(out, "login keychain: missing") {
		t.Fatalf("key show after rm: %q", out)
	}
	if _, errs := h.mustFail(1, "key", "rm", "-f"); !strings.Contains(errs, "no keychain item") {
		t.Fatalf("key rm with nothing to delete: %q", errs)
	}

	if out := h.mustRun("key", "restore"); !strings.Contains(out, "saved to the login keychain") {
		t.Fatalf("key restore: %q", out)
	}
	h.mustRun("ls")
	if _, errs := h.mustFail(1, "key", "restore"); !strings.Contains(errs, "already in the login keychain") {
		t.Fatalf("key restore twice: %q", errs)
	}

	h.app.readSecret = func(string) ([]byte, error) { return []byte("new"), nil }
	if out := h.mustRun("key", "passphrase"); !strings.Contains(out, "recovery passphrase changed") {
		t.Fatalf("key passphrase: %q", out)
	}
	if rec := h.lastSave(); rec.Action != "key passphrase" || rec.Generation != h.generation() {
		t.Fatalf("record after key passphrase: %+v", rec)
	}
	h.mustRun("key", "rm", "-f")
	h.app.readSecret = func(string) ([]byte, error) { return []byte("pw"), nil }
	if _, errs := h.mustFail(1, "key", "restore"); errs == "" {
		t.Fatal("restore with the old passphrase succeeded")
	}
	h.app.readSecret = func(string) ([]byte, error) { return []byte("new"), nil }
	h.mustRun("key", "restore")
	h.mustRun("ls")
	if _, errs := h.mustFail(2, "key", "bogus"); !strings.Contains(errs, "unknown action") {
		t.Fatalf("key bogus: %q", errs)
	}
}
