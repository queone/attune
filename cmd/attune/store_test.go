package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/queone/gkit/color"
	"github.com/queone/gkit/lockbox"
)

// testKDF keeps passphrase derivation fast in tests.
var testKDF = lockbox.KDF{Time: 1, Memory: 8 * 1024, Threads: 1}

// testNow is the fixed clock every harness starts with.
var testNow = time.Date(2026, 9, 23, 17, 55, 2, 0, time.Local)

// line renders the plain, padded status line the CLI prints for an entry.
func line(word, name string) string {
	return fmt.Sprintf("%-*s%s\n", statusWidth, word, name)
}

// harness drives every command in-process against a private home, a
// private copy of the testdata specs, and a memory key store.
type harness struct {
	t     *testing.T
	app   *app
	out   *bytes.Buffer
	errb  *bytes.Buffer
	root  string
	home  string
	specs string
	cfg   string
	store string
	keys  *lockbox.MemoryKeyStore
}

func testEnv(home string) lockbox.Env {
	return lockbox.Env{
		Home:       home,
		ConfigHome: filepath.Join(home, ".config"),
		DataHome:   filepath.Join(home, ".local", "share"),
		StateHome:  filepath.Join(home, ".local", "state"),
		CacheHome:  filepath.Join(home, ".cache"),
		ClaudeDir:  filepath.Join(home, ".config", "claude"),
	}
}

// copySpecs copies testdata/specs into dst and writes a configuration file
// beside it that adds content_version: test.
func copySpecs(t *testing.T, root string) (specs, cfg string) {
	t.Helper()
	specs = filepath.Join(root, "specs")
	if err := os.MkdirAll(specs, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("testdata/specs")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("testdata/specs", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(specs, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("testdata/attune.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg = filepath.Join(root, "attune.yaml")
	if err := os.WriteFile(cfg, append(raw, []byte("content_version: test\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	return specs, cfg
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	synced := filepath.Join(root, "synced")
	for _, d := range []string{filepath.Join(home, ".config"), synced} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	specs, cfg := copySpecs(t, root)
	h := &harness{t: t, out: &bytes.Buffer{}, errb: &bytes.Buffer{}, root: root, home: home, specs: specs, cfg: cfg,
		store: filepath.Join(synced, "attune.store"), keys: &lockbox.MemoryKeyStore{}}
	h.app = &app{
		stdout: h.out, stderr: h.errb, stdin: strings.NewReader(""), keys: h.keys, env: testEnv(home), host: "np10",
		kdf: testKDF, goos: "darwin",
		isTerminal: func() bool { return true },
		readSecret: func(string) ([]byte, error) { return []byte("pw"), nil },
		readLine:   func(string) (string, error) { return "", nil },
		editor:     func(string) error { return nil },
		now:        func() time.Time { return testNow },
	}
	return h
}

// run executes attune with -t naming the harness store, appended so it
// follows the verb.
func (h *harness) run(args ...string) (int, string, string) {
	return h.runRaw(append(append([]string{}, args...), "-t", h.store)...)
}

// runRaw executes attune with the arguments as given, no -t added.
func (h *harness) runRaw(args ...string) (int, string, string) {
	h.out.Reset()
	h.errb.Reset()
	code := h.app.run(args)
	return code, h.out.String(), h.errb.String()
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	code, out, errs := h.run(args...)
	if code != 0 {
		h.t.Fatalf("attune %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errs)
	}
	return out
}

func (h *harness) mustFail(want int, args ...string) (string, string) {
	h.t.Helper()
	code, out, errs := h.run(args...)
	if code != want {
		h.t.Fatalf("attune %s: exit %d, want %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, want, out, errs)
	}
	return out, errs
}

// initAndAdd creates the store and captures the configuration and specs.
func (h *harness) initAndAdd() {
	h.t.Helper()
	h.mustRun("init", "-N")
	h.mustRun("add", "attune.yaml", h.cfg)
	h.mustRun("add", h.specs)
}

func (h *harness) spec(name string) string { return filepath.Join(h.specs, name) }

func (h *harness) read(p string) string {
	h.t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// lsNames returns the NAME column of an ls listing.
func lsNames(out string) []string {
	var names []string
	for i, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if i == 0 {
			continue
		}
		fields := strings.Fields(l)
		names = append(names, fields[len(fields)-1])
	}
	return names
}

const validateStoreLine = "attune validate: OK (6 specs) content=test\n"

var storeNames = []string{"app.yaml", "attune.yaml", "dns.yaml", "group.yaml", "resource-group.yaml", "role-assignment.yaml", "role-definition.yaml"}

func TestStoreRoundTripsTheSpecs(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("init", "-N")
	for _, want := range []string{"store created at " + h.store, "key saved to the login keychain", "store path remembered in " + h.app.pointerFile()} {
		if !strings.Contains(out, want) {
			t.Fatalf("init lacks %q: %q", want, out)
		}
	}
	if out := h.mustRun("add", "attune.yaml", h.cfg); out != line("added", "attune.yaml") {
		t.Fatalf("add attune.yaml %q", out)
	}
	out = h.mustRun("add", h.specs)
	if !strings.Contains(out, line("added", "app.yaml")) || !strings.Contains(out, line("added", "dns.yaml")) {
		t.Fatalf("add specs %q", out)
	}
	if got := lsNames(h.mustRun("ls")); strings.Join(got, ",") != strings.Join(storeNames, ",") {
		t.Fatalf("ls names %v, want %v", got, storeNames)
	}
	if got, want := h.mustRun("cat", "app.yaml"), h.read(h.spec("app.yaml")); got != want {
		t.Fatal("cat app.yaml differs from the file it came from")
	}
	if _, errs := h.mustFail(1, "add", "app.yaml", h.spec("app.yaml")); !strings.Contains(errs, "already registered") {
		t.Fatalf("second add: %q", errs)
	}
	if out := h.mustRun("rename", "app.yaml", "apps/app.yaml"); !strings.Contains(out, line("renamed", "app.yaml -> apps/app.yaml")) {
		t.Fatalf("rename output %q", out)
	}
	if got := h.mustRun("cat", "apps/app.yaml"); got != h.read(h.spec("app.yaml")) {
		t.Fatal("rename lost the stored content")
	}
	if _, errs := h.mustFail(1, "rename", "dns.yaml", "apps/app.yaml"); !strings.Contains(errs, "already registered") {
		t.Fatalf("rename onto a held name: %q", errs)
	}
	if _, errs := h.mustFail(2, "rename", "dns.yaml", "../x.yaml"); !strings.Contains(errs, "not a spec name") {
		t.Fatalf("rename to a bad name: %q", errs)
	}
	if out := h.mustRun("rm", "apps/app.yaml"); out != "removed apps/app.yaml\n" {
		t.Fatalf("rm output %q", out)
	}
	if got := lsNames(h.mustRun("ls")); len(got) != 6 || got[0] != "attune.yaml" {
		t.Fatalf("ls after rm %v", got)
	}
	if got := lsNames(h.mustRun("ls", "-b", "captured")); len(got) != 6 {
		t.Fatalf("ls -b captured %v", got)
	}
	if _, errs := h.mustFail(2, "ls", "-b", "size"); !strings.Contains(errs, "usage") {
		t.Fatalf("ls bad field: %q", errs)
	}
}

func TestValidateReadsOnlyTheStore(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	t.Chdir(t.TempDir())
	if code, out, errs := h.runRaw("validate"); code != 0 || out != validateStoreLine {
		t.Fatalf("remembered store: code %d out %q err %q", code, out, errs)
	}
	if out := h.mustRun("validate"); out != validateStoreLine {
		t.Fatalf("-t: %q", out)
	}
	if code, _, errs := h.runRaw("validate", "-s", "x"); code != 2 || !strings.Contains(errs, "unknown flag") {
		t.Fatalf("-s must be gone: code %d err %q", code, errs)
	}
	missing := filepath.Join(h.root, "missing.store")
	h.app.storeEnv = missing
	code, _, errs := h.runRaw("validate")
	if code != 1 || !strings.Contains(errs, missing) || !strings.Contains(errs, "attune init") {
		t.Fatalf("missing store: code %d err %q", code, errs)
	}
	h.app.storeEnv = ""
	if err := os.Remove(h.spec("app.yaml")); err != nil {
		t.Fatal(err)
	}
	if out := h.mustRun("validate"); out != validateStoreLine {
		t.Fatalf("validate after deleting a live file must not change: %q", out)
	}
}

func TestAddValidatesBeforeSaving(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	broken := filepath.Join(h.root, "broken.yaml")
	if err := os.WriteFile(broken, []byte("kind: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errs := h.mustFail(1, "add", "zones/broken.yaml", broken); !strings.Contains(errs, "parse zones/broken.yaml") {
		t.Fatalf("broken yaml: %q", errs)
	}
	if _, errs := h.mustFail(1, "add", "copies/dns.yaml", h.spec("dns.yaml")); !strings.Contains(errs, "duplicate resource key") {
		t.Fatalf("duplicate key: %q", errs)
	}
	if got := lsNames(h.mustRun("ls")); len(got) != 7 {
		t.Fatalf("refused adds changed the store: %v", got)
	}
	if _, errs := h.mustFail(2, "add", h.spec("dns.yaml")); !strings.Contains(errs, "attune add NAME FILE") {
		t.Fatalf("single file without a name: %q", errs)
	}
	h.mustRun("rm", "dns.yaml")
	h.app.stdin = strings.NewReader(h.read(h.spec("dns.yaml")))
	h.app.isTerminal = func() bool { return false }
	if out := h.mustRun("add", "zones/dns.yaml"); out != line("added", "zones/dns.yaml") {
		t.Fatalf("add from stdin %q", out)
	}
	if got := h.mustRun("cat", "zones/dns.yaml"); got != h.read(h.spec("dns.yaml")) {
		t.Fatal("stdin content differs")
	}
	h.app.isTerminal = func() bool { return true }
	if _, errs := h.mustFail(2, "add", "zones/other.yaml"); !strings.Contains(errs, "pipe the content") {
		t.Fatalf("stdin on a terminal: %q", errs)
	}
	if _, errs := h.mustFail(2, "add", "nope.txt"); !strings.Contains(errs, "not a spec name") {
		t.Fatalf("bad name: %q", errs)
	}
}

func TestStShowsStoreState(t *testing.T) {
	h := newHarness(t)
	if out, _ := h.mustFail(1, "st"); !strings.Contains(out, "store file: missing") || !strings.Contains(out, "store opens: no") {
		t.Fatalf("st before init %q", out)
	}
	h.initAndAdd()
	out := h.mustRun("st")
	for _, want := range []string{"store: " + h.store + " (flag)", "remembered: " + h.app.pointerFile() + " -> " + h.store, "login keychain: present", "store opens: yes", "entries: 7", "conflict copies: none", "last save here: in store (generation 3, 2026-09-23 17:55:02)", "drift: run attune plan to compare with Azure"} {
		if !strings.Contains(out, want) {
			t.Fatalf("st lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "bundle") || strings.Contains(out, "specs ") {
		t.Fatalf("st mentions a live directory: %s", out)
	}
	if _, errs := h.mustFail(2, "st", "extra"); !strings.Contains(errs, "takes no arguments") {
		t.Fatalf("st with an argument: %q", errs)
	}
}

func TestEveryCommandIsMacOSOnly(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	h.app.goos = "linux"
	for _, verb := range append([]string{"validate", "plan", "apply"}, storeVerbs...) {
		if code, _, errs := h.run(verb); code != 1 || !strings.Contains(errs, "attune store support is macOS only") {
			t.Fatalf("%s on linux: code %d stderr %q", verb, code, errs)
		}
	}
	if code, out, _ := h.runRaw("version"); code != 0 || out != "attune v"+programVersion+"\n" {
		t.Fatalf("version on linux: code %d out %q", code, out)
	}
	if code, out, _ := h.runRaw("help"); code != 0 || !strings.HasPrefix(out, "attune v") {
		t.Fatalf("help on linux: code %d out %q", code, out)
	}
}

func TestUnlockOnAnotherMac(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	h.app.keys = &lockbox.MemoryKeyStore{}
	h.app.env = testEnv(filepath.Join(h.root, "home2"))
	if _, errs := h.mustFail(1, "ls"); !strings.Contains(errs, "run `attune init`") {
		t.Fatalf("ls without the key: %q", errs)
	}
	if out := h.mustRun("init"); !strings.Contains(out, "saved to the login keychain") || !strings.Contains(out, "remembered in") {
		t.Fatalf("init unlock %q", out)
	}
	if got := lsNames(h.mustRun("ls")); len(got) != 7 {
		t.Fatalf("ls after unlock %v", got)
	}
	h.app.readSecret = func(string) ([]byte, error) { return []byte("wrong"), nil }
	h.app.keys = &lockbox.MemoryKeyStore{}
	if _, errs := h.mustFail(1, "init"); errs == "" {
		t.Fatal("init with the wrong passphrase succeeded")
	}
}

// generation returns the generation in the harness store's header.
func (h *harness) generation() uint64 {
	h.t.Helper()
	b, err := os.ReadFile(h.store)
	if err != nil {
		h.t.Fatal(err)
	}
	hdr, err := lockbox.ParseHeader(b)
	if err != nil {
		h.t.Fatal(err)
	}
	return hdr.Generation
}

// keyID returns the key id in the harness store's header.
func (h *harness) keyID() string {
	h.t.Helper()
	b, err := os.ReadFile(h.store)
	if err != nil {
		h.t.Fatal(err)
	}
	hdr, err := lockbox.ParseHeader(b)
	if err != nil {
		h.t.Fatal(err)
	}
	return lockbox.KeyIDString(hdr.KeyID)
}

// lastSave reads this Mac's last-save record for the harness store.
func (h *harness) lastSave() lockbox.LastSave {
	h.t.Helper()
	b, err := os.ReadFile(h.app.lastSaveFile(h.keyID()))
	if err != nil {
		h.t.Fatal(err)
	}
	var rec lockbox.LastSave
	if err := json.Unmarshal(b, &rec); err != nil {
		h.t.Fatal(err)
	}
	return rec
}

// replaceWithOtherMacSave plays another Mac: it opens the store bytes in
// base, saves them, and puts the result where the harness store was, the way
// iCloud Drive replaces one Mac's copy with another's.
func (h *harness) replaceWithOtherMacSave(base []byte) {
	h.t.Helper()
	other := filepath.Join(h.root, "other.store")
	if err := os.WriteFile(other, base, 0o600); err != nil {
		h.t.Fatal(err)
	}
	hdr, err := lockbox.ParseHeader(base)
	if err != nil {
		h.t.Fatal(err)
	}
	st, err := lockbox.Load(other, h.keys.Keys[lockbox.KeyIDString(hdr.KeyID)])
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := st.SaveLogged("np11", "edit dns.yaml", testNow); err != nil {
		h.t.Fatal(err)
	}
	st.Close()
	if err := os.Rename(other, h.store); err != nil {
		h.t.Fatal(err)
	}
}

func lostWarning(rec lockbox.LastSave) string {
	return fmt.Sprintf("attune: warning: this Mac's last save (%s at %s) is missing from the store; another Mac's copy replaced it. Run it again.\n",
		rec.Action, rec.At.Local().Format(time.DateTime))
}

func TestEveryAttuneSaveRecordsTheCommand(t *testing.T) {
	h := newHarness(t)
	h.mustRun("init", "-N")
	check := func(want string) {
		t.Helper()
		rec := h.lastSave()
		if rec.Action != want || rec.Generation != h.generation() || len(rec.ID) != 32 || !rec.At.Equal(testNow) {
			t.Fatalf("record %+v, want action %q at generation %d", rec, want, h.generation())
		}
	}
	check("init")
	path := h.app.lastSaveFile(h.keyID())
	if filepath.Dir(path) != filepath.Join(h.home, ".local", "state", "attune") {
		t.Fatalf("record path %s", path)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record file: %v %v", info, err)
	}
	h.mustRun("add", "attune.yaml", h.cfg)
	check("add attune.yaml " + h.cfg)
	h.mustRun("add", h.specs)
	check("add " + h.specs)
	h.mustRun("rm", "dns.yaml")
	check("rm dns.yaml")
	h.app.stdin = strings.NewReader(h.read(h.spec("dns.yaml")))
	h.app.isTerminal = func() bool { return false }
	h.mustRun("add", "zones/dns.yaml")
	check("add zones/dns.yaml")
	h.app.isTerminal = func() bool { return true }
	h.mustRun("rename", "app.yaml", "apps/app.yaml")
	check("rename app.yaml apps/app.yaml")

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "content_version") || strings.Contains(string(b), "-t") || strings.Contains(string(b), h.store) {
		t.Fatalf("record holds file contents or the store option: %s", b)
	}
	if _, errs := h.mustFail(0, "ls"); errs != "" {
		t.Fatalf("ls warned after this Mac's own saves: %q", errs)
	}
}

func TestAttuneLostSaveIsReportedUntilThisMacSavesAgain(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	base, err := os.ReadFile(h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.mustRun("rename", "app.yaml", "apps/app.yaml")
	lost := h.lastSave()
	h.replaceWithOtherMacSave(base)
	if h.generation() != lost.Generation {
		t.Fatalf("fixture: store generation %d, lost save generation %d", h.generation(), lost.Generation)
	}

	out, errs := h.mustFail(1, "st")
	wantLost := fmt.Sprintf("lost: rename app.yaml apps/app.yaml at 2026-09-23 17:55:02 (generation %d); run it again", lost.Generation)
	if !strings.Contains(out, "last save here: "+wantLost+"\n") || errs != "" {
		t.Fatalf("st after a lost save: stdout %q stderr %q", out, errs)
	}
	restore := color.SetEnabled(true)
	_, out, _ = h.run("st")
	red := color.Red5(wantLost)
	restore()
	if !strings.Contains(out, "last save here: "+red+"\n") {
		t.Fatalf("lost save not red: %q", out)
	}

	code, out, errs := h.run("ls")
	if code != 0 || errs != lostWarning(lost) || !strings.Contains(out, "app.yaml") {
		t.Fatalf("ls after a lost save: code %d stdout %q stderr %q", code, out, errs)
	}
	if _, _, errs := h.run("cat", "dns.yaml"); errs != lostWarning(lost) {
		t.Fatalf("cat after a lost save: stderr %q", errs)
	}
	if code, out, errs := h.run("validate"); code != 0 || out != validateStoreLine || errs != lostWarning(lost) {
		t.Fatalf("validate after a lost save: code %d stdout %q stderr %q", code, out, errs)
	}

	code, _, errs = h.run("rm", "dns.yaml")
	if code != 0 || errs != lostWarning(lost) {
		t.Fatalf("a later save: code %d stderr %q", code, errs)
	}
	next := h.lastSave()
	if next.Action != "rm dns.yaml" || next.ID == lost.ID {
		t.Fatalf("record after the next save: %+v", next)
	}
	if _, errs := h.mustFail(0, "ls"); errs != "" {
		t.Fatalf("warning outlived the next save: %q", errs)
	}
	if out := h.mustRun("st"); !strings.Contains(out, fmt.Sprintf("last save here: in store (generation %d, 2026-09-23 17:55:02)\n", next.Generation)) {
		t.Fatalf("st after the next save: %q", out)
	}
}

func TestAttuneLastSaveStaysInStoreUnderAnotherMacsLaterSave(t *testing.T) {
	h := newHarness(t)
	h.initAndAdd()
	mine := h.lastSave()
	current, err := os.ReadFile(h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.replaceWithOtherMacSave(current)
	if h.generation() != mine.Generation+1 {
		t.Fatalf("fixture: generation %d", h.generation())
	}
	want := fmt.Sprintf("in store (generation %d, 2026-09-23 17:55:02)", mine.Generation)
	out, errs := h.mustFail(0, "st")
	if !strings.Contains(out, "last save here: "+want+"\n") || errs != "" {
		t.Fatalf("st under a later save: stdout %q stderr %q", out, errs)
	}
	restore := color.SetEnabled(true)
	_, out, _ = h.run("st")
	green := color.Grn5(want)
	restore()
	if !strings.Contains(out, "last save here: "+green+"\n") {
		t.Fatalf("in store not green: %q", out)
	}
}

func TestAttuneLastSaveNoneAndUnknown(t *testing.T) {
	h := newHarness(t)
	h.mustRun("init", "-N")
	keyID := h.keyID()
	if err := os.Remove(h.app.lastSaveFile(keyID)); err != nil {
		t.Fatal(err)
	}
	if out := h.mustRun("st"); !strings.Contains(out, "last save here: none\n") {
		t.Fatalf("st without a record: %q", out)
	}
	stale := lockbox.SaveRecord{ID: strings.Repeat("0", 32), Generation: 0, At: testNow, Action: "init"}
	if err := lockbox.RecordLastSave(h.app.lastSaveFile(keyID), stale); err != nil {
		t.Fatal(err)
	}
	if out := h.mustRun("st"); !strings.Contains(out, "last save here: unknown (older than the save log)\n") {
		t.Fatalf("st with a record older than the log: %q", out)
	}
	h.keys.Keys = map[string][]byte{}
	if out, _ := h.mustFail(1, "st"); !strings.Contains(out, "last save here: unknown\n") {
		t.Fatalf("st without a key: %q", out)
	}
}

func TestAttuneStStartsWithTheCheckTime(t *testing.T) {
	h := newHarness(t)
	out, _ := h.mustFail(1, "st")
	if first, _, _ := strings.Cut(out, "\n"); first != "checked: 2026-09-23 17:55:02" {
		t.Fatalf("first st line %q", first)
	}
}
